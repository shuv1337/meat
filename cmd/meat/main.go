// Command meat abridges a code diff into a "reading diff": the same change,
// rewritten to keep only what a senior reviewer actually needs to read —
// mechanical noise (batch field copies, error-message construction, forced
// zero-value returns, generated code) elided, behavior-bearing changes kept.
//
// Usage:
//
//	# summarize the default change in the current repo (git HEAD / jj @)
//	meat
//
//	# summarize a specific commit, revision, or jj revset
//	meat <sha>
//	meat HEAD~3
//	meat '@-'
//	meat 'trunk()::@'
//
//	# diff across a commit range (git) or revset (jj)
//	meat <sha1>..<sha2>
//	meat main...HEAD
//
//	# current / parent change
//	meat -w
//	meat current
//	meat parent
//
//	# staged changes (git only)
//	meat -staged
//
//	# abridge any diff piped on stdin
//	git show <sha> | meat
//	jj diff --git | meat
//
// It reads OPENAI_API_KEY or ANTHROPIC_API_KEY from the environment
// (optionally the matching provider base URL, plus MEAT_MODEL / -model),
// or subscription OAuth tokens from `meat login openai|anthropic`.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"meat.dev/meat"
)

const usage = `meat — abridge a diff into a "reading diff"

Usage:
  meat                 Summarize the default change (git HEAD, or jj @).
  meat <revision>      Summarize a git revision/range or jj revset.
  meat current         Summarize the current change (git worktree / jj @).
  meat parent          Summarize the parent change (git HEAD / jj @-).
  meat -staged         Abridge staged (index) changes — git only.
  meat -w              Abridge the current change (same as meat current).
  git show <sha> | meat   Abridge the diff piped on stdin.
  jj diff --git | meat    Abridge a jj Git-format diff piped on stdin.

  meat login openai [--device]   Log in with ChatGPT Plus/Pro subscription.
  meat login anthropic           Log in with Claude Pro/Max subscription.
  meat logout [openai|anthropic] Remove stored OAuth credentials.
  meat auth status               Show OAuth credential status.

meat reads a unified diff (from stdin, from a named target, or the backend
default when stdin is a terminal), asks an LLM to drop everything not worth
reading, and prints the abridged diff plus a one-line summary.

Backend selection (first match wins):
  1. -vcs=git|jj
  2. MEAT_VCS=git|jj
  3. Auto-detect: deepest repository root wins; colocated jj/git prefers jj.

In a colocated repository jj is the default. Use -vcs=git for Git index
semantics. current and parent are reserved keywords that shadow same-named
refs; reach a Git branch named parent as refs/heads/parent, or a jj bookmark
as bookmarks(exact:"parent").

jj targets are passed verbatim to jj diff --git -r <revset>. Quote revsets
that the shell would expand, e.g. meat '@-' or meat 'trunk()::@'.
jj has no staging area: -staged fails under jj.
jj diff reads snapshot the working copy (including background prewarm).

Results are cached under ~/.meat keyed by the SHA of (rubric/compiler protocol +
model + diff contents), so re-running on an unchanged diff is instant; editing
the diff, switching models, or upgrading meat's rubric or compiler invariants
recomputes.

On an interactive terminal the diff is colored and paged like git show (using
your git pager and color.diff config when available); piped/redirected output
stays plain.

Flags:
  -model string   Model to use (default $MEAT_MODEL or a built-in default).
  -no-cache       Ignore any cached result and recompute (still updates cache).
  -staged         Read staged changes (git only).
  -w              Read the current change (git worktree / jj @).
  -vcs string     Backend: auto (default), git, or jj.
  -summary        Print only the short plain-language summary.
  -json           Emit the result as JSON on stdout (no color, no pager).
  -h, --help      Show this help.

Environment:
  OPENAI_API_KEY       API key for OpenAI models (including the default).
  OPENAI_BASE_URL      Optional. Override the OpenAI API base URL.
  ANTHROPIC_API_KEY    API key for Claude models.
  ANTHROPIC_BASE_URL   Optional. Override the Anthropic API base URL.
  MEAT_MODEL           Optional. Default model id.
  MEAT_VCS             Optional. Backend preference: auto, git, or jj.
  MEAT_CACHE           Optional. Cache directory (default ~/.meat; empty disables).
  MEAT_AUTH_FILE       Optional. OAuth credential store (default ~/.meat/auth.json).

Auth precedence: explicit API key / base URL, then stored OAuth (meat login),
then the exe.dev managed LLM gateway when available.
`

func main() {
	if tryAuthCommand(os.Args[1:]) {
		return
	}

	fs := flag.NewFlagSet("meat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	model := fs.String("model", "", "model to use (default $MEAT_MODEL or built-in default)")
	noCache := fs.Bool("no-cache", false, "ignore any cached result and recompute (still updates the cache)")
	staged := fs.Bool("staged", false, "read staged changes (git only)")
	worktree := fs.Bool("w", false, "read the current change (git worktree / jj @)")
	vcsFlag := fs.String("vcs", "", "backend: auto, git, or jj (default auto; also MEAT_VCS)")
	summaryOnly := fs.Bool("summary", false, "print only the short plain-language summary")
	jsonOut := fs.Bool("json", false, "emit the result as JSON on stdout")
	if err := fs.Parse(os.Args[1:]); err != nil {
		// flag already printed the error and (for -h) the usage.
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2)
	}
	if *summaryOnly && *jsonOut {
		fatal("-summary and -json are mutually exclusive")
	}

	pref, err := parseVCSPreference(*vcsFlag, os.Getenv("MEAT_VCS"))
	if err != nil {
		fatal("%v", err)
	}
	repo, err := detectRepository(pref)
	if err != nil {
		fatal("%v", err)
	}

	diff, source, err := readDiff(repo, fs.Args(), *staged, *worktree)
	if err != nil {
		fatal("%v", err)
	}

	empty := strings.TrimSpace(diff) == ""
	if empty && !*jsonOut {
		fatal("no diff to read (%s)", source)
	}

	// Progress feedback is strictly interactive: only when BOTH stdout and
	// stderr are terminals (so `meat > file` and `meat 2> log` stay clean),
	// and never in -json mode.
	progress := func(string) {}
	interactive := !*jsonOut && isTerminal(os.Stdout) && isTerminal(os.Stderr)
	if interactive {
		progress = func(msg string) {
			// Overwrite a single status line on stderr; cleared before render.
			fmt.Fprintf(os.Stderr, "\r\x1b[Kmeat: %s", msg)
		}
	}

	vcsLabel := string(repo.kind)
	if source == "stdin" && repo.kind == "" {
		vcsLabel = ""
	}

	if empty {
		// Structured empty success: no model, no cache, zero tokens.
		res := &meat.Result{Summary: emptySummary(source)}
		if interactive {
			fmt.Fprint(os.Stderr, "\r\x1b[K")
		}
		renderJSONMeta(os.Stdout, res, "", vcsLabel, source, true)
		return
	}

	// compute produces a fresh result on a cache miss. It is a closure so run()
	// can be unit-tested without an LLM: the real path constructs the selected
	// provider model (which needs credentials/network) only here, AFTER the cache
	// check.
	compute := func(ctx context.Context) (*meat.Result, error) {
		m, err := meat.NewModelFromEnv(ctx, *model)
		if err != nil {
			return nil, err
		}
		// Confine the read-only tools to the repo root, when we're in one, so
		// the agent can inspect surrounding source for clues.
		return meat.Abridge(ctx, m, meat.Request{
			RepoRoot:    repo.root,
			RepoBackend: string(repo.kind),
			UnifiedDiff: diff,
			Progress:    progress,
		})
	}

	render := func(res *meat.Result) {
		if interactive {
			fmt.Fprint(os.Stderr, "\r\x1b[K") // clear the progress line
		}
		elision := meat.ElisionLine(diff, res.SmartDiff)
		if *jsonOut {
			renderJSONMeta(os.Stdout, res, elision, vcsLabel, source, false)
			return
		}
		if *summaryOnly {
			renderSummary(os.Stdout, res)
			return
		}
		// On an interactive terminal, render like `git show`: git's diff colors
		// and git's pager. Otherwise (piped/redirected) print plain text.
		renderResult(os.Stdout, res, elision)
	}

	opts := runOpts{
		diff:     diff,
		model:    meat.ResolveModel(*model),
		rubric:   meat.RubricHash(),
		cacheDir: cacheDir(),
		noCache:  *noCache,
		compute:  compute,
		render:   render,
		stderr:   os.Stderr,
	}
	if err := run(context.Background(), opts); err != nil {
		if interactive {
			fmt.Fprint(os.Stderr, "\r\x1b[K")
		}
		fatal("%v", err)
	}
}

// emptySummary is the human-readable summary for a valid empty target.
func emptySummary(source string) string {
	switch source {
	case "staged":
		return "No staged changes."
	case "worktree":
		return "No unstaged changes."
	case "stdin":
		return "No changes."
	default:
		if source == "" {
			return "No changes."
		}
		return fmt.Sprintf("No changes in %s.", source)
	}
}

// runOpts carries everything run needs, so the orchestration (cache lookup,
// compute-on-miss, store, print) can be tested in isolation from flag parsing,
// git, and the LLM.
type runOpts struct {
	diff     string
	model    string // already resolved (post $MEAT_MODEL/default)
	rubric   string // rubric hash, part of the cache key
	cacheDir string
	noCache  bool
	// compute produces a fresh result on a cache miss. The real implementation
	// constructs the LLM-backed model; it must only be called on a miss.
	compute func(ctx context.Context) (*meat.Result, error)
	// render emits the result body (summary + diff) to the user.
	render func(*meat.Result)
	stderr io.Writer
}

// run is the cache-aware core: look up by SHA of (model + diff); on a hit,
// print and return without touching opts.compute (so cache hits are instant and
// need no credentials); on a miss, compute, store, and print. -no-cache skips
// the read but still writes through.
func run(ctx context.Context, o runOpts) error {
	key := cacheKey(o.diff, o.model, o.rubric)
	if !o.noCache {
		if res, ok := cacheLoad(o.cacheDir, key); ok {
			o.render(res)
			fmt.Fprintf(o.stderr, "\nmeat: cached (sha %s)\n", key[:12])
			return nil
		}
	}

	start := time.Now()
	res, err := o.compute(ctx)
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Round(100 * time.Millisecond)

	cacheStore(o.cacheDir, key, res)

	o.render(res)
	fmt.Fprintf(o.stderr, "\nmeat: tokens in=%d out=%d in %s\n", res.InputTokens, res.OutputTokens, elapsed)
	return nil
}

func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	// Package errors are already prefixed "meat:"; don't double it up.
	if !strings.HasPrefix(msg, "meat:") {
		msg = "meat: " + msg
	}
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
