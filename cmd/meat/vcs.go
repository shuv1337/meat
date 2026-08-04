package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// vcsKind is the selected version-control backend.
type vcsKind string

const (
	vcsGit vcsKind = "git"
	vcsJJ  vcsKind = "jj"
)

// repository is the selected backend and its canonical workspace/repo root.
type repository struct {
	kind vcsKind
	root string
}

// parseVCSPreference resolves the user-selected backend preference from the
// CLI flag and MEAT_VCS. Empty and "auto" mean automatic detection. Any other
// unrecognized value is an error.
func parseVCSPreference(flagValue, envValue string) (string, error) {
	pref := strings.TrimSpace(flagValue)
	if pref == "" {
		pref = strings.TrimSpace(envValue)
	}
	switch strings.ToLower(pref) {
	case "", "auto":
		return "auto", nil
	case "git":
		return "git", nil
	case "jj":
		return "jj", nil
	default:
		src := "-vcs"
		if strings.TrimSpace(flagValue) == "" {
			src = "MEAT_VCS"
		}
		return "", fmt.Errorf("invalid %s value %q (want auto, git, or jj)", src, pref)
	}
}

// detectRepository selects a backend according to preference:
//   - "git" / "jj": require that backend; never fall back
//   - "auto": deepest root wins; same root (colocated) prefers jj
//
// Detection never snapshots a jj working copy.
func detectRepository(pref string) (repository, error) {
	switch pref {
	case "git":
		root, err := gitToplevel()
		if err != nil {
			return repository{}, fmt.Errorf("git repository not found: %w", err)
		}
		return repository{kind: vcsGit, root: root}, nil
	case "jj":
		root, err := jjWorkspaceRoot()
		if err != nil {
			return repository{}, fmt.Errorf("jj repository not found: %w", err)
		}
		return repository{kind: vcsJJ, root: root}, nil
	case "auto":
		jjRoot, jjErr := jjWorkspaceRoot()
		gitRoot, gitErr := gitToplevel()
		switch {
		case jjErr == nil && gitErr == nil:
			if deeperRoot(jjRoot, gitRoot) == gitRoot && gitRoot != jjRoot {
				return repository{kind: vcsGit, root: gitRoot}, nil
			}
			// Same root (colocated) or jj deeper → jj.
			return repository{kind: vcsJJ, root: jjRoot}, nil
		case jjErr == nil:
			return repository{kind: vcsJJ, root: jjRoot}, nil
		case gitErr == nil:
			return repository{kind: vcsGit, root: gitRoot}, nil
		default:
			return repository{}, nil
		}
	default:
		return repository{}, fmt.Errorf("invalid vcs preference %q", pref)
	}
}

// deeperRoot returns the path that is a descendant of the other, or a if equal
// / incomparable. Roots are cleaned before comparison.
func deeperRoot(a, b string) string {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return a
	}
	sep := string(os.PathSeparator)
	if strings.HasPrefix(a+sep, b+sep) || strings.HasPrefix(a, b+sep) {
		return a
	}
	if strings.HasPrefix(b+sep, a+sep) || strings.HasPrefix(b, a+sep) {
		return b
	}
	// Unrelated paths: prefer the longer path string as a weak signal of depth.
	if len(b) > len(a) {
		return b
	}
	return a
}

func gitToplevel() (string, error) {
	out, err := runVCS("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// jjWorkspaceRoot discovers the jj workspace root without snapshotting.
func jjWorkspaceRoot() (string, error) {
	out, err := runVCS("", "jj", "workspace", "root", "--ignore-working-copy")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runVCS runs a VCS command and returns stdout. On failure the error names the
// command family and includes plain stderr text.
func runVCS(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(ee.Stderr))
			if stderr == "" {
				return "", fmt.Errorf("%s %s: exit status %d", name, strings.Join(args, " "), ee.ExitCode())
			}
			return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), stderr)
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// diffResult is a acquired unified diff plus its human-readable source label.
type diffResult struct {
	diff   string
	source string
}

// readDiff returns the diff to abridge for the selected backend.
//
// Precedence:
//   - -staged / -w / current keyword
//   - parent keyword
//   - an explicit revision/revset argument
//   - stdin, when piped
//   - otherwise the backend default (git HEAD / jj @)
func readDiff(repo repository, args []string, staged, worktree bool) (string, string, error) {
	if staged && worktree {
		return "", "", fmt.Errorf("-staged and -w are mutually exclusive")
	}
	if (staged || worktree) && len(args) > 0 {
		return "", "", fmt.Errorf("-staged/-w cannot be combined with a revision argument")
	}
	if len(args) > 1 {
		return "", "", fmt.Errorf("too many arguments: want at most one revision, got %d", len(args))
	}

	if staged {
		return readStaged(repo)
	}
	if worktree {
		return readCurrent(repo)
	}
	if len(args) == 1 {
		target := args[0]
		switch strings.ToLower(target) {
		case "current":
			return readCurrent(repo)
		case "parent":
			return readParent(repo)
		default:
			return readTarget(repo, target)
		}
	}
	if stdinIsPiped() {
		data, err := readAllStdin()
		if err != nil {
			return "", "stdin", err
		}
		return string(data), "stdin", nil
	}
	return readDefault(repo)
}

func readStaged(repo repository) (string, string, error) {
	switch repo.kind {
	case vcsGit:
		out, err := runVCS(repo.root, "git", "diff", "--staged")
		if err != nil {
			return "", "staged", fmt.Errorf("reading staged changes: %w", err)
		}
		return out, "staged", nil
	case vcsJJ:
		return "", "staged", fmt.Errorf("jj has no staging area; use -w/current, or select -vcs=git in a colocated repository")
	default:
		return "", "staged", fmt.Errorf("reading staged changes: not in a git repository")
	}
}

func readCurrent(repo repository) (string, string, error) {
	switch repo.kind {
	case vcsGit:
		out, err := runVCS(repo.root, "git", "diff")
		if err != nil {
			return "", "worktree", fmt.Errorf("reading working-tree changes: %w", err)
		}
		return out, "worktree", nil
	case vcsJJ:
		out, err := jjDiff(repo.root, "@")
		if err != nil {
			return "", "@", fmt.Errorf("reading current change: %w", err)
		}
		return out, "@", nil
	default:
		return "", "worktree", fmt.Errorf("reading working-tree changes: not in a repository")
	}
}

func readParent(repo repository) (string, string, error) {
	switch repo.kind {
	case vcsGit:
		out, err := gitShow(repo.root, "HEAD")
		if err != nil {
			return "", "HEAD", fmt.Errorf("reading HEAD: %w", err)
		}
		return out, "HEAD", nil
	case vcsJJ:
		out, err := jjDiff(repo.root, "@-")
		if err != nil {
			return "", "@-", fmt.Errorf("reading parent change: %w", err)
		}
		return out, "@-", nil
	default:
		return "", "parent", fmt.Errorf("reading parent change: not in a repository")
	}
}

func readDefault(repo repository) (string, string, error) {
	switch repo.kind {
	case vcsGit:
		out, err := gitShow(repo.root, "HEAD")
		if err != nil {
			return "", "HEAD", fmt.Errorf("reading HEAD (are you in a git repo?): %w", err)
		}
		return out, "HEAD", nil
	case vcsJJ:
		out, err := jjDiff(repo.root, "@")
		if err != nil {
			return "", "@", fmt.Errorf("reading @ (are you in a jj repo?): %w", err)
		}
		return out, "@", nil
	default:
		return "", "", fmt.Errorf("not in a git or jj repository (pipe a unified diff, or run inside a repository)")
	}
}

func readTarget(repo repository, target string) (string, string, error) {
	switch repo.kind {
	case vcsGit:
		var out string
		var err error
		if isRevRange(target) {
			out, err = runVCS(repo.root, "git", "diff", target)
		} else {
			out, err = gitShow(repo.root, target)
		}
		if err != nil {
			return "", target, fmt.Errorf("reading %q: %w", target, err)
		}
		return out, target, nil
	case vcsJJ:
		out, err := jjDiff(repo.root, target)
		if err != nil {
			return "", target, fmt.Errorf("reading %q: %w", target, err)
		}
		return out, target, nil
	default:
		return "", target, fmt.Errorf("reading %q: not in a repository", target)
	}
}

// gitShow shows one commit's diff. Plain `git show` on a merge commit emits NO
// diff (so meat would report "no diff to read"); -m --first-parent makes a
// merge show its diff against the first parent — i.e. "what did merging this
// branch change on main" — and leaves regular commits untouched.
func gitShow(dir, rev string) (string, error) {
	return runVCS(dir, "git", "show", "--format=fuller", "-m", "--first-parent", rev)
}

// jjDiff returns a Git-format unified diff for a jj revset. The revset is
// passed verbatim; Meat does not parse or rewrite it. Snapshotting is allowed
// so current-change reads stay fresh.
func jjDiff(dir, revset string) (string, error) {
	return runVCS(dir, "jj", "diff", "--git", "--color=never", "-r", revset)
}

// isRevRange reports whether rev uses git's range syntax (A..B or A...B), as
// opposed to a single revision. Only consulted on the Git backend.
func isRevRange(rev string) bool {
	return strings.Contains(rev, "..")
}

// git is retained for tests and helpers that still speak Git directly.
func git(args ...string) (string, error) {
	return runVCS("", "git", args...)
}
