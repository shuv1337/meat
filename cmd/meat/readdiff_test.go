package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func runJJ(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("jj", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"JJ_CONFIG=/dev/null",
		"JJ_EMAIL=t@t",
		"JJ_USER=t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not found: %v", err)
	}
}

func requireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skipf("jj not found: %v", err)
	}
}

func initGitRepo(t *testing.T) (dir string, repo repository) {
	t.Helper()
	requireGit(t)
	dir = t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "first commit: add a.txt")
	t.Chdir(dir)
	return dir, repository{kind: vcsGit, root: dir}
}

func initJJRepo(t *testing.T) (dir string, repo repository) {
	t.Helper()
	requireJJ(t)
	dir = t.TempDir()
	// Pure jj workspace: git store is hidden under .jj (not a colocated checkout).
	runJJ(t, dir, "git", "init", "--no-colocate")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, dir, "commit", "-m", "first")
	t.Chdir(dir)
	root, err := jjWorkspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	return dir, repository{kind: vcsJJ, root: root}
}

// TestReadDiffRevisionArg verifies that `meat <revision>` summarizes the named
// commit, exercising the real `git show` path against a throwaway repo.
func TestReadDiffRevisionArg(t *testing.T) {
	dir, repo := initGitRepo(t)
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = dir
	shaCmd.Env = gitEnv()
	shaOut, err := shaCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(shaOut))

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "b.txt")
	runGit(t, dir, "commit", "-q", "-m", "second commit: add b.txt")

	diff, source, err := readDiff(repo, []string{sha}, false, false)
	if err != nil {
		t.Fatalf("readDiff(%q): %v", sha, err)
	}
	if source != sha {
		t.Errorf("source = %q, want %q", source, sha)
	}
	if !strings.Contains(diff, "a.txt") || !strings.Contains(diff, "first commit") {
		t.Errorf("diff does not describe the requested commit:\n%s", diff)
	}
	if strings.Contains(diff, "b.txt") {
		t.Errorf("diff leaked a later commit (b.txt); arg not honored:\n%s", diff)
	}
}

// TestReadDiffRevRange verifies that `meat A..B` diffs across the range rather
// than showing a single commit.
func TestReadDiffRevRange(t *testing.T) {
	dir, repo := initGitRepo(t)
	baseCmd := exec.Command("git", "rev-parse", "HEAD")
	baseCmd.Dir = dir
	baseCmd.Env = gitEnv()
	baseOut, err := baseCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(baseOut))

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "b.txt")
	runGit(t, dir, "commit", "-q", "-m", "second commit: add b.txt")

	for _, rng := range []string{base + "..HEAD", base + "...HEAD"} {
		diff, source, err := readDiff(repo, []string{rng}, false, false)
		if err != nil {
			t.Fatalf("readDiff(%q): %v", rng, err)
		}
		if source != rng {
			t.Errorf("source = %q, want %q", source, rng)
		}
		if !strings.Contains(diff, "b.txt") {
			t.Errorf("range %q diff missing b.txt:\n%s", rng, diff)
		}
		if strings.Contains(diff, "second commit") {
			t.Errorf("range %q produced commit metadata; used 'git show' not 'git diff':\n%s", rng, diff)
		}
	}
}

// TestReadDiffBadRevision surfaces a useful error for an unknown revision.
func TestReadDiffBadRevision(t *testing.T) {
	_, repo := initGitRepo(t)
	if _, _, err := readDiff(repo, []string{"deadbeef"}, false, false); err == nil {
		t.Fatal("readDiff with unknown revision: want error, got nil")
	}
}

// TestReadDiffTooManyArgs rejects more than one revision.
func TestReadDiffTooManyArgs(t *testing.T) {
	if _, _, err := readDiff(repository{}, []string{"a", "b"}, false, false); err == nil {
		t.Fatal("readDiff with two args: want error, got nil")
	}
}

// TestReadDiffMergeCommit: plain `git show` on a merge commit emits no diff at
// all, which used to make meat report "no diff to read". The first-parent diff
// (what merging the branch changed on main) must come through.
func TestReadDiffMergeCommit(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	t.Chdir(dir)
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "base")
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feat.txt"), []byte("feature work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "feat.txt")
	runGit(t, dir, "commit", "-q", "-m", "feature commit")
	runGit(t, dir, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "main.txt")
	runGit(t, dir, "commit", "-q", "-m", "main commit")
	runGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	repo := repository{kind: vcsGit, root: dir}
	diff, _, err := readDiff(repo, nil, false, false) // HEAD is the merge
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "feat.txt") {
		t.Errorf("merge commit produced no first-parent diff (feat.txt missing):\n%s", diff)
	}
	if !strings.Contains(diff, "merge feature") {
		t.Errorf("merge commit metadata missing:\n%s", diff)
	}
}

// TestReadDiffStagedAndWorktree exercises -staged (index vs HEAD) and -w
// (working tree vs index) against a real repo.
func TestReadDiffStagedAndWorktree(t *testing.T) {
	dir, repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2 staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "a.txt")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v3 worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stagedDiff, source, err := readDiff(repo, nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != "staged" {
		t.Errorf("source = %q, want staged", source)
	}
	if !strings.Contains(stagedDiff, "v2 staged") || strings.Contains(stagedDiff, "v3 worktree") {
		t.Errorf("-staged should show index-vs-HEAD only:\n%s", stagedDiff)
	}

	wtDiff, source, err := readDiff(repo, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if source != "worktree" {
		t.Errorf("source = %q, want worktree", source)
	}
	if !strings.Contains(wtDiff, "v3 worktree") || strings.Contains(wtDiff, "-v1") {
		t.Errorf("-w should show worktree-vs-index only:\n%s", wtDiff)
	}
}

// TestReadDiffFlagConflicts rejects nonsensical flag combinations.
func TestReadDiffFlagConflicts(t *testing.T) {
	if _, _, err := readDiff(repository{}, nil, true, true); err == nil {
		t.Error("-staged with -w: want error")
	}
	if _, _, err := readDiff(repository{}, []string{"HEAD"}, true, false); err == nil {
		t.Error("-staged with a revision: want error")
	}
	if _, _, err := readDiff(repository{}, []string{"HEAD"}, false, true); err == nil {
		t.Error("-w with a revision: want error")
	}
}

func TestReadDiffParentAndCurrentKeywords(t *testing.T) {
	dir, repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// parent on git → HEAD commit (includes a.txt from first commit)
	diff, source, err := readDiff(repo, []string{"parent"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != "HEAD" {
		t.Errorf("parent source = %q, want HEAD", source)
	}
	if !strings.Contains(diff, "a.txt") {
		t.Errorf("parent diff missing a.txt:\n%s", diff)
	}

	// current on git → worktree
	diff, source, err = readDiff(repo, []string{"current"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != "worktree" {
		t.Errorf("current source = %q, want worktree", source)
	}
	if !strings.Contains(diff, "edited") {
		t.Errorf("current diff missing worktree edit:\n%s", diff)
	}
}

func TestParseVCSPreference(t *testing.T) {
	cases := []struct {
		flag, env, want string
		err             bool
	}{
		{"", "", "auto", false},
		{"auto", "git", "auto", false}, // flag wins when set
		{"", "git", "git", false},
		{"jj", "git", "jj", false},
		{"GIT", "", "git", false},
		{"bogus", "", "", true},
		{"", "bogus", "", true},
	}
	for _, tc := range cases {
		got, err := parseVCSPreference(tc.flag, tc.env)
		if tc.err {
			if err == nil {
				t.Errorf("parseVCSPreference(%q,%q): want error", tc.flag, tc.env)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseVCSPreference(%q,%q): %v", tc.flag, tc.env, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseVCSPreference(%q,%q) = %q, want %q", tc.flag, tc.env, got, tc.want)
		}
	}
}

func TestDetectGitOnly(t *testing.T) {
	dir, _ := initGitRepo(t)
	// Ensure no jj workspace.
	if _, err := os.Stat(filepath.Join(dir, ".jj")); err == nil {
		t.Fatal("unexpected .jj in git-only fixture")
	}
	repo, err := detectRepository("auto")
	if err != nil {
		t.Fatal(err)
	}
	if repo.kind != vcsGit {
		t.Fatalf("kind = %q, want git", repo.kind)
	}
	if filepath.Clean(repo.root) != filepath.Clean(dir) {
		t.Errorf("root = %q, want %q", repo.root, dir)
	}
}

func TestDetectPureJJ(t *testing.T) {
	dir, _ := initJJRepo(t)
	repo, err := detectRepository("auto")
	if err != nil {
		t.Fatal(err)
	}
	if repo.kind != vcsJJ {
		t.Fatalf("kind = %q, want jj (got root %q)", repo.kind, repo.root)
	}
	if filepath.Clean(repo.root) != filepath.Clean(dir) {
		t.Errorf("root = %q, want %q", repo.root, dir)
	}
}

func TestDetectColocatedPrefersJJ(t *testing.T) {
	requireGit(t)
	requireJJ(t)
	dir := t.TempDir()
	runJJ(t, dir, "git", "init", "--colocate")
	t.Chdir(dir)
	repo, err := detectRepository("auto")
	if err != nil {
		t.Fatal(err)
	}
	if repo.kind != vcsJJ {
		t.Fatalf("colocated kind = %q, want jj", repo.kind)
	}
}

func TestDetectVCSGitOverrideOnColocated(t *testing.T) {
	requireGit(t)
	requireJJ(t)
	dir := t.TempDir()
	runJJ(t, dir, "git", "init", "--colocate")
	t.Chdir(dir)
	repo, err := detectRepository("git")
	if err != nil {
		t.Fatal(err)
	}
	if repo.kind != vcsGit {
		t.Fatalf("kind = %q, want git", repo.kind)
	}
}

func TestDetectVCSJJRefusesGitOnly(t *testing.T) {
	initGitRepo(t)
	if _, err := detectRepository("jj"); err == nil {
		t.Fatal("jj preference in git-only repo: want error")
	}
}

func TestDetectNestedGitInsideJJ(t *testing.T) {
	requireGit(t)
	requireJJ(t)
	outer := t.TempDir()
	runJJ(t, outer, "git", "init", "--no-colocate")
	inner := filepath.Join(outer, "nested")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, inner, "init", "-q")
	t.Chdir(inner)
	repo, err := detectRepository("auto")
	if err != nil {
		t.Fatal(err)
	}
	if repo.kind != vcsGit {
		t.Fatalf("nested git inside jj: kind = %q, want git", repo.kind)
	}
	if filepath.Clean(repo.root) != filepath.Clean(inner) {
		t.Errorf("root = %q, want %q", repo.root, inner)
	}
}

func TestDetectNoOpLogOnDiscovery(t *testing.T) {
	requireJJ(t)
	dir := t.TempDir()
	runJJ(t, dir, "git", "init", "--no-colocate")
	t.Chdir(dir)

	// Record op log length before detection.
	before := jjOpCount(t, dir)
	if _, err := detectRepository("auto"); err != nil {
		t.Fatal(err)
	}
	after := jjOpCount(t, dir)
	if after != before {
		t.Errorf("detection changed op log count %d → %d", before, after)
	}
}

func jjOpCount(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("jj", "op", "log", "--ignore-working-copy", "--limit", "100", "-T", `id.short() ++ "\n"`)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JJ_CONFIG=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("jj op log: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func TestJJCurrentAndParent(t *testing.T) {
	dir, repo := initJJRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Track the new file so it appears in @.
	runJJ(t, dir, "file", "track", "b.txt")

	diff, source, err := readDiff(repo, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if source != "@" {
		t.Errorf("source = %q, want @", source)
	}
	if !strings.Contains(diff, "b.txt") {
		t.Errorf("-w/@ diff missing newly tracked b.txt:\n%s", diff)
	}
	if !strings.Contains(diff, "diff --git") {
		t.Errorf("jj diff missing git format:\n%s", diff)
	}

	// parent → @-
	diff, source, err = readDiff(repo, []string{"parent"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != "@-" {
		t.Errorf("parent source = %q, want @-", source)
	}
	if !strings.Contains(diff, "a.txt") {
		t.Errorf("parent/@- diff missing a.txt:\n%s", diff)
	}
}

func TestJJStagedRejected(t *testing.T) {
	_, repo := initJJRepo(t)
	_, _, err := readDiff(repo, nil, true, false)
	if err == nil {
		t.Fatal("jj -staged: want error")
	}
	if !strings.Contains(err.Error(), "no staging area") {
		t.Errorf("error = %v, want staging-area message", err)
	}
}

func TestJJRevsetTarget(t *testing.T) {
	dir, repo := initJJRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, dir, "commit", "-m", "second")

	// After two commits, @-- is the first (a.txt); @- is the second (c.txt).
	diff, source, err := readDiff(repo, []string{"@--"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != "@--" {
		t.Errorf("source = %q, want @--", source)
	}
	if !strings.Contains(diff, "a.txt") {
		t.Errorf("@-- diff missing a.txt:\n%s", diff)
	}

	// Contiguous revset describing only the second change.
	diff, source, err = readDiff(repo, []string{"@-"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != "@-" {
		t.Errorf("source = %q, want @-", source)
	}
	if !strings.Contains(diff, "c.txt") {
		t.Errorf("@- diff missing c.txt:\n%s", diff)
	}
}

func TestJJInvalidRevset(t *testing.T) {
	_, repo := initJJRepo(t)
	_, _, err := readDiff(repo, []string{"this-is-not-a-valid-revset-zzz"}, false, false)
	if err == nil {
		t.Fatal("invalid revset: want error")
	}
	if !strings.Contains(err.Error(), "jj") {
		t.Errorf("error should name jj: %v", err)
	}
}

func TestJJBookmarkTarget(t *testing.T) {
	dir, repo := initJJRepo(t)
	runJJ(t, dir, "bookmark", "create", "feature", "-r", "@-")
	diff, source, err := readDiff(repo, []string{"feature"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if source != "feature" {
		t.Errorf("source = %q, want feature", source)
	}
	if !strings.Contains(diff, "a.txt") {
		t.Errorf("bookmark diff missing a.txt:\n%s", diff)
	}
}

func TestEmptySummary(t *testing.T) {
	cases := map[string]string{
		"staged":   "No staged changes.",
		"worktree": "No unstaged changes.",
		"@":        "No changes in @.",
		"stdin":    "No changes.",
		"":         "No changes.",
	}
	for src, want := range cases {
		if got := emptySummary(src); got != want {
			t.Errorf("emptySummary(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestDeeperRoot(t *testing.T) {
	if got := deeperRoot("/a", "/a/b"); got != "/a/b" {
		t.Errorf("deeperRoot(/a,/a/b) = %q", got)
	}
	if got := deeperRoot("/a/b", "/a"); got != "/a/b" {
		t.Errorf("deeperRoot(/a/b,/a) = %q", got)
	}
	if got := deeperRoot("/a", "/a"); got != "/a" {
		t.Errorf("deeperRoot equal = %q", got)
	}
}
