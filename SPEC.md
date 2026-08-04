# First-Class Jujutsu Support

## Status

Reviewed and approved for implementation (2026-08-04). This document describes
behavior and an implementation sequence; it does not imply that the changes
have been made.

## Objective

Make Meat work natively in Git repositories, pure Jujutsu repositories, and
colocated jj/Git repositories. `opencode-meat` must inherit the same behavior
without implementing its own VCS detection or diff acquisition.

The supported jj baseline is the locally installed `jj 0.40.0` behavior. The
implementation may support older versions when that falls out naturally, but
must not add compatibility branches without a demonstrated need.

## Current Behavior

Meat currently assumes Git throughout its repository-aware path:

- `cmd/meat/main.go` acquires `HEAD`, revision, range, staged, and worktree
  diffs with Git commands.
- `cmd/meat/main.go` discovers repository context with `git rev-parse`.
- `meat/tools.go` implements model-assisted search with `git grep`.
- `opencode/index.ts` independently fingerprints `git diff` before prewarming.
- `opencode/core.ts`, `opencode/index.ts`, and `opencode/tui.ts` expose Git terms
  such as `staged`, `worktree`, and `HEAD`.

Consequences:

- Pure jj repositories cannot provide a foreground diff or repository context.
- Prewarming silently does nothing in pure jj repositories.
- Colocated repositories use Git index/HEAD semantics even when jj is the
  user's active VCS.
- The plugin's prewarm input can drift from Meat's foreground input.
- Empty-result plugin copy assumes Git staging semantics.

## Goals

- Auto-detect Git and jj repositories from any nested working directory.
- Prefer jj in a colocated repository.
- Allow an explicit Git or jj override.
- Accept native jj change IDs, commit IDs, bookmarks, and revsets.
- Preserve Git behavior when Git is selected.
- Feed Git-format unified diffs to the existing Meat pipeline for both VCSes.
- Preserve model-assisted repository context in pure jj repositories.
- Make prewarming use exactly the same VCS selection, target normalization,
  model, diff bytes, and cache key as foreground execution.
- Give users clear errors for concepts that do not exist in the selected VCS.
- Keep piped unified-diff input VCS-neutral and unchanged.

## Non-Goals

- Reimplementing jj revset parsing.
- Translating arbitrary Git revision expressions into jj revsets or vice versa.
- Adding staging semantics to jj.
- Parsing jj's native color-word diff format.
- Changing the reading-diff rubric, compiler, or model-selection behavior.
- Replacing Git hosting, npm publishing, GitHub workflows, or remote operations
  with jj-specific APIs.
- Redesigning interactive colors and pager selection in the first release.
- Making cache entries repository-specific as part of this change.

## Terminology

- **Backend**: the selected `git` or `jj` implementation.
- **Current change**: Git's unstaged working-tree diff when Git is selected, or
  the working-copy commit `@` when jj is selected.
- **Parent change**: Git `HEAD` when Git is selected, or jj `@-` when jj is
  selected.
- **Target**: a backend-native Git revision/range or jj revset supplied by the
  user.
- **Colocated repository**: a jj repository backed by a Git repository in the
  same working copy.

## Required User-Facing Behavior

### Backend Selection

Selection precedence must be:

1. Explicit CLI `-vcs=jj` or `-vcs=git`.
2. `MEAT_VCS=jj` or `MEAT_VCS=git`.
3. Automatic root comparison: run both `jj workspace root
   --ignore-working-copy` and `git rev-parse --show-toplevel` from the current
   directory. If both succeed with different roots, select the backend whose
   root is deeper (closer to the current directory). If both succeed with the
   same root (a colocated repository), select jj. If exactly one succeeds,
   select it.
4. No repository backend.

Detection must not snapshot the jj working copy: root discovery passes
`--ignore-working-copy`, which is safe because the workspace root does not
depend on snapshot state. This keeps mere detection from writing jj operation
log entries — including the case where a plain Git repository nested inside a
jj workspace resolves to the Git backend.

`-vcs=auto` and `MEAT_VCS=auto` must use automatic detection. An empty value is
equivalent to `auto`. Any other value must fail before model or cache access.

If the user explicitly selects a backend, Meat must not silently fall back to
the other backend. Missing executable, non-repository, and invalid-target
errors must name the selected backend.

Because a root tie resolves to jj, a colocated repository uses jj by default.
Users may still request Git index semantics with `-vcs=git`.

### Target Semantics

| Invocation | Git backend | jj backend |
| --- | --- | --- |
| `meat` | `git show --format=fuller -m --first-parent HEAD` | `jj diff --git -r @` |
| `meat -w` | `git diff` | `jj diff --git -r @` |
| `meat current` | Same as `-w` | Same as `-w` |
| `meat parent` | Git `HEAD` change | `jj diff --git -r @-` |
| `meat <target>` | Existing Git revision/range behavior | `jj diff --git -r <target>` |
| `meat -staged` | `git diff --staged` | Unsupported with actionable error |
| piped diff | Read stdin | Read stdin |

The jj backend must pass the complete positional target as one argument to
`jj`; Meat must not parse or rewrite the revset. In particular, the Git
backend's `..` range detection must not run on the jj path: `A..B` and `A::B`
are jj revset operators with their own semantics and travel verbatim as one
`-r` value. Shell quoting remains the caller's responsibility, for example:

```sh
meat '@-'
meat 'trunk()::@'
meat 'bookmarks(exact:"main")..@'
```

The existing one-positional-target limit remains. `-w` and `-staged` remain
mutually exclusive with each other and with a positional target.

`current` and `parent` are reserved positional keywords on both backends and
unconditionally shadow refs with the same name. Meat must not probe whether a
same-named ref exists. Documentation must state the escape hatches: a Git
branch literally named `parent` is reachable as `refs/heads/parent`, and a jj
bookmark literally named `parent` as `bookmarks(exact:"parent")`.

On jj, `-staged` must fail with copy equivalent to:

```text
meat: jj has no staging area; use -w/current, or select -vcs=git in a colocated repository
```

Git `HEAD`, `A..B`, and `A...B` behavior must remain unchanged when Git is
selected. Meat must not special-case `HEAD` for jj; users should use jj-native
targets such as `@` and `@-`.

### Diff Format

Every jj-generated diff sent to the Meat library must use `--git` and
`--color=never`. The existing parser continues to consume Git-format unified
diffs. Native jj color-word output must never enter the reading-diff pipeline.

For jj merge commits, `jj diff -r`'s documented behavior applies: compare the
automatic merge of all parent trees with the selected revision. Git retains
its existing first-parent merge behavior.

### Working-Copy Snapshot Behavior

The jj backend must allow normal jj working-copy snapshotting. It must not pass
`--ignore-working-copy` for current-change reads because doing so can return a
stale diff. Snapshotting and operation-log updates are accepted consequences of
reading the current jj change.

All background calls remain debounced and cancellable to reduce snapshots of
intermediate edits. Background prewarm therefore produces Meat-visible jj
operation log entries during active editing; this is accepted jj-native
behavior and must be stated in the documentation.

### Empty Diffs and JSON

Human-mode behavior must continue to return a non-zero `no diff to read`
error; scripts and shell users keep the existing exit-code signal. Only in
`-json` mode must an empty valid target be a successful structured result,
not an error inferred by consumers from stderr.

The JSON wire result must add:

```json
{
  "vcs": "jj",
  "source": "@",
  "empty": true,
  "summary": "No changes in @.",
  "smart_diff": "",
  "input_tokens": 0,
  "output_tokens": 0
}
```

Requirements:

- `vcs` is `git`, `jj`, or omitted for piped input outside a repository.
- `source` is the normalized human-readable source used for the invocation.
- `empty` is always present in new JSON output.
- Empty results use zero tokens and do not construct a model.
- Non-empty cached and computed results include the same metadata.
- Existing fields and snake_case naming remain compatible.
- Additional metadata does not become part of the persistent cache payload.

## Meat Architecture

### Backend Representation

Add a small internal backend seam in `cmd/meat`; do not create a general VCS
framework. The seam must own:

- Backend detection.
- Repository/workspace root discovery.
- Current-change diff acquisition.
- Parent-change diff acquisition.
- Arbitrary target diff acquisition.
- Git-only staged acquisition.

The implementation may use a compact struct or interface. It must make command
selection independently testable without invoking a model.

Proposed internal concepts:

```go
type vcsKind string

const (
    vcsGit vcsKind = "git"
    vcsJJ  vcsKind = "jj"
)

type repository struct {
    kind vcsKind
    root string
}
```

Names are proposed, not mandatory. Behavior in this specification is
mandatory.

### Diff Acquisition

Refactor the current `readDiff` orchestration in `cmd/meat/main.go` so it
receives the selected backend. Keep stdin precedence and flag-conflict checks
explicit.

Command failures must include the command family and target without dumping
raw transport or serialized error objects. stderr from Git or jj may be
included as plain diagnostic text.

### Repository Context

Replace `gitRoot()` with the selected backend's canonical root. Pass both root
and backend identity into the Meat request so contextual tools know how to
enumerate searchable files.

`meat.Request` currently carries `RepoRoot`. Extend it with the minimum
backend information needed by `meat/tools.go`; do not expose command execution
through the public request.

### Contextual Search

Git behavior remains `git grep`.

For jj, search only files present in the `@` tree:

1. Enumerate paths with `jj file list -r @` from the workspace root.
2. Apply the requested path restriction before opening files.
3. Skip non-regular files and binary files.
4. Match with Go's RE2 regular-expression behavior.
5. Preserve `path:line:text` output, 200-line cap, and existing tool-output
   truncation.
6. Preserve root confinement and reject escaping paths.

The search implementation must not walk `.jj`, `.git`, ignored build output,
or arbitrary untracked files. `read_file` continues to read a requested path
from the working copy after root confinement.

The model-facing search description must no longer call the operation
`git grep` when jj is selected, and must state the active pattern language
accurately for each backend: the Git backend's `git grep` invocation uses
POSIX basic regular expressions, while the jj backend matches with Go RE2.
Unifying both backends on in-process RE2 search is explicitly deferred as
follow-up work; this specification must not change Git search semantics.

### Cache Identity

Do not add backend, root, source, change ID, or commit ID to the persistent
cache key. The existing key of rubric hash, resolved model, and exact diff
bytes remains authoritative. Identical semantic inputs may share cached output
across backends; different Git-format bytes naturally miss.

Repository-context sensitivity in cache identity is an existing broader issue
and is outside this specification.

### Rendering

Git-format output continues through the existing renderer. Git-configured
colors and pager lookup may remain as compatibility behavior even for pure jj
repositories. If Git is unavailable, existing fallback rendering must remain
functional. Native jj colors and pager configuration are deferred.

## opencode-meat Architecture

### Delegation Boundary

`opencode-meat` must not execute `git` or `jj` for diff acquisition,
fingerprinting, backend detection, or root discovery.

Remove the `git diff` subprocess and SHA-256 fingerprint from
`opencode/index.ts`. A scheduled prewarm invokes the same `runMeat` path as a
foreground request with target `current`. Meat's persistent cache determines
whether inference is needed.

Repeated post-debounce invocations against an unchanged diff are acceptable
cache hits. The plugin must retain:

- Per-session-directory scheduling.
- Debouncing.
- Cancellation of obsolete subprocesses.
- At most one active prewarm per directory.
- Idle acceleration.
- Silent best-effort background failures.

### Plugin Targets

The tool and TUI descriptions must advertise:

```text
current (default), parent, staged (Git only), or a Git revision/range or jj revset
```

Target normalization in `opencode/core.ts` must be limited to neutral aliases:

- Empty, `current`, `worktree`, `working-tree`, and `unstaged` invoke `-w`.
- `parent` is passed to Meat as the neutral positional alias.
- `staged` and `index` invoke `-staged`.
- Other non-flag values pass through as one positional target.
- Values beginning with `-` remain rejected as targets.

Do not map `head` or `latest` in the plugin. Those are Git-specific and would
silently select the wrong jj change. Existing users can pass literal `HEAD`
when Git is selected.

### Plugin Options

Add an optional plugin `vcs` option with values `auto`, `git`, or `jj`. When
present, foreground, TUI, and prewarm calls pass `-vcs=<value>` to Meat. The
server and TUI configurations must use the same value when both are installed.

If absent, Meat performs automatic detection. `model` behavior remains
unchanged.

### Plugin Result Handling

Extend `resultSchema` for `vcs`, `source`, and `empty`. `source` and `target`
remain distinct fields: `source` is Meat's normalized statement of what was
actually read (for example `@`, `worktree`, or `stdin`), while `target` stays
the plugin-injected record of what the user requested. The plugin prefers
`source` for display when present.

Remove the synthesized `No staged/unstaged changes` results and render the
structured Meat result verbatim. One narrow stderr check remains for error
copy only: when Meat exits non-zero and stderr contains `no diff to read`
(an old binary paired with the new plugin), the plugin must throw a message
telling the user to update Meat or that there are no changes, instead of the
raw error. It must not synthesize a success result from stderr.

Synthetic-message metadata should include normalized target and VCS when
available.

## Documentation

Update:

- `README.md`
- CLI usage and environment text in `cmd/meat/main.go`
- `opencode/README.md`
- `opencode/TESTING-HANDOFF.md`
- Git-only narrowing advice in `meat/meat.go` and `meat/chunk.go`
- Model-facing tool descriptions in `meat/tools.go`

Documentation must include:

- Auto-detection and colocated jj precedence.
- `-vcs` and `MEAT_VCS` overrides.
- `@`, `@-`, bookmark, and quoted revset examples.
- The reserved `current`/`parent` keywords and their ref escape hatches.
- The lack of a jj staging area.
- The fact that jj diff reads snapshot the working copy, including visible
  operation log entries from background prewarm.
- The per-backend search pattern language (git grep BRE vs Go RE2).
- Matching `model` and `vcs` options across server and TUI plugin config.
- Git compatibility and unchanged piped-diff behavior.

## Test Requirements

### Go Unit and Integration Tests

Extend or split `cmd/meat/readdiff_test.go` to cover:

- Git-only repository auto-detection.
- Pure jj repository auto-detection.
- Colocated repository preferring jj.
- A Git repository nested inside a jj workspace selecting Git (deepest root).
- Root discovery not creating jj operation log entries (detection uses
  `--ignore-working-copy`).
- `-vcs=git` overriding colocated preference.
- `-vcs=jj` refusing a Git-only repository.
- Invalid `-vcs` and `MEAT_VCS` values.
- CLI option taking precedence over the environment.
- Nested-directory root discovery for both backends.
- jj default/current `@` diff including newly tracked files.
- jj `parent` resolving to `@-`.
- jj change ID and bookmark targets.
- A quoted contiguous jj revset.
- Invalid jj revset diagnostics.
- jj `-staged` actionable rejection.
- Existing Git `HEAD`, merge, range, staged, and worktree tests remaining green.
- Empty JSON results for Git and jj without model construction.
- JSON metadata on fresh and cached non-empty results.

Add contextual-tool tests in `meat/meat_test.go` or a focused new test file for:

- Search in a pure jj repository.
- Path restriction and root confinement.
- Ignored/untracked exclusion.
- Binary-file exclusion.
- No-match behavior and output cap.

Tests that require jj must skip with an explicit reason when `jj` is not on
`PATH`. CI should install jj so those tests normally execute.

### Diff Compatibility Fixtures

Add jj `--git` fixtures or integration assertions for:

- Added, modified, and deleted files.
- Rename behavior.
- Symlinks where supported.
- Conflicted files.
- Multi-parent changes.

The tests must prove that the existing parser/compiler accepts the output; do
not add a second jj-specific diff parser.

### Plugin Tests

Update `opencode/index.test.ts` and add focused `core` tests to verify:

- Prewarm invokes Meat directly and never invokes `git` or `jj` itself.
- Rapid mutation events still debounce to one Meat invocation.
- A later unchanged invocation is allowed and uses the foreground cache path.
- Cancellation and one-active-job behavior remain intact.
- `model` and `vcs` are forwarded identically in foreground and prewarm calls.
- Neutral target alias mapping.
- Structured empty-result rendering.
- Git and jj metadata passes into synthetic messages.

## Validation Commands

Run from the repository root:

```sh
go test ./...
```

Expected: all Go tests pass, including jj integration tests when jj is
installed.

Run from `opencode/`:

```sh
bun test
bun run typecheck
npm pack --dry-run
```

Expected: plugin tests and typechecking pass, and the tarball contains only the
intended runtime files and documentation.

Run formatting and whitespace checks:

```sh
gofmt -w <changed-go-files>
git diff --check
```

Perform manual smoke tests in disposable Git-only, pure jj, and colocated
repositories:

```sh
meat -json -w
meat -json parent
meat -json '<native-target>'
```

In a colocated repository, compare automatic selection with:

```sh
meat -json -vcs=jj -w
meat -json -vcs=git -w
```

Finally verify `/meat`, `/meat parent`, an unchanged cached repeat, and one
native jj revset through the TUI using `termctrl`.

## Implementation Sequence

1. Add backend selection, root discovery, and command construction with tests.
2. Refactor diff acquisition and implement jj current/parent/revset behavior.
3. Add structured JSON metadata and empty-result success behavior.
4. Add jj-aware contextual search and pure jj integration coverage.
5. Remove VCS logic from plugin prewarming and add neutral target/config support.
6. Update documentation, package version, and testing handoff.
7. Run automated tests and disposable-repository smoke tests.
8. Publish only after Git, pure jj, colocated, prewarm, and TUI checks pass.

Each step must keep existing Git tests green. Do not combine a package publish
with unverified implementation work.

## Acceptance Criteria

- `meat -w` succeeds in a pure jj repository and reads `@` using Git-format
  output.
- `meat '@-'` and a multi-change jj revset produce the intended reading input.
- A colocated repository selects jj by default and Git when explicitly forced.
- Git-only users retain current HEAD, range, staged, and worktree behavior.
- jj staged requests fail before model construction with an actionable message.
- Model context root and search work without a `.git` directory.
- Piped unified diffs remain unchanged and require neither Git nor jj.
- Empty `-json` requests return structured success without spending tokens.
- Foreground and prewarm calls share the same model, VCS, exact diff, and cache.
- `opencode-meat` contains no subprocess invocation of `git` or `jj`.
- Plugin UI no longer labels jj empty changes as staged or unstaged.
- All automated and manual validations described above pass.

## Risks and Mitigations

- **jj snapshot races with active edits**: retain debounce, cancellation, and
  idle acceleration; never use stale `--ignore-working-copy` output.
- **Revset ambiguity**: pass the target directly to jj rather than interpreting
  it in Meat.
- **Colocated behavior change**: document jj-first detection and provide
  `-vcs=git`/`MEAT_VCS=git`.
- **Different merge semantics**: document backend-native semantics and cover
  both with tests.
- **Parser incompatibility for uncommon jj changes**: require `--git` and add
  conflict/multi-parent fixtures before release.
- **Plugin process overhead after removing fingerprints**: rely on Meat's
  content cache; unchanged local cache hits are expected to remain cheap.
- **Context search semantic differences**: enumerate the jj `@` tree rather
  than walking the filesystem, and pin behavior with tests.

## Rollback

The implementation must remain separable enough that jj auto-detection can be
disabled by setting `MEAT_VCS=git` or plugin `vcs: "git"`. If jj support causes
release regressions, restore Git as the documented default while retaining the
backend seam and jj tests for follow-up. Cache files require no migration or
rollback because their key format remains unchanged.

## Review Decisions

Resolved in review on 2026-08-04; the sections above already incorporate the
outcomes:

1. **Colocated repositories prefer jj by default** — confirmed, refined to
   deepest-root-wins detection with jj winning root ties.
2. **Bare `meat` reads Git `HEAD` but jj `@`** — confirmed; no fallback to
   `@-` when `@` is empty.
3. **jj positional targets use `jj diff --git -r <revset>` without Meat
   parsing** — confirmed; the Git `..` range detection never runs on the jj
   path.
4. **`-staged` is rejected under jj rather than silently using Git** —
   confirmed; hard failure before model or cache access, even in colocated
   repositories.
5. **The plugin removes its own diff fingerprint and delegates cache decisions
   to Meat** — confirmed; prewarm-driven jj snapshots are accepted and
   documented.
6. **Empty `-json` diffs become successful structured results** — confirmed;
   human mode must keep its non-zero `no diff to read` exit. The plugin keeps
   one stderr check purely to produce an actionable version-skew error message.

Additional resolutions from review:

7. **Detection never snapshots** — root discovery uses
   `--ignore-working-copy`.
8. **`current`/`parent` are reserved keywords** — they shadow same-named refs
   unconditionally, with documented escape hatches.
9. **`source` and `target` stay distinct** — CLI-emitted actual read versus
   plugin-recorded request.
10. **Search regex flavors stay split** — git grep BRE for Git, Go RE2 for jj,
    with backend-accurate tool descriptions; RE2 unification is deferred
    follow-up.
