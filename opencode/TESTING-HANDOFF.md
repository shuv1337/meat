# OpenCode Meat Plugin Testing Handoff

## Goal

Verify the next `opencode-meat` package end to end in OpenCode V2: server and
TUI plugin discovery, direct `/meat` execution, native tool execution, project
directory resolution, clean/error UX, Git + jj backends, and a real reading
diff.

## Prepared State

- Repository: `/home/shuv/repos/meat`
- Plugin package: `opencode-meat@0.3.0`
- Plugin ID: `meat` / `meat.tui`
- Meat binary rebuilt from this checkout with first-class jj support
- Go tests (`go test ./...`) and plugin checks (`bun test`, `bun run typecheck`)
  passed before handoff

Do not edit the global OpenCode config. Do not republish during this test.

## Restart

From `/home/shuv/repos/meat`:

```sh
shuvcode service restart
shuvcode service status
```

## Discovery Checks

```sh
shuvcode api get /api/plugin \
  --header 'x-opencode-directory:%2Fhome%2Fshuv%2Frepos%2Fmeat'
```

Expected:

- `/api/plugin` includes `{ "id": "meat" }`.
- The TUI plugin list includes `{ "id": "meat.tui" }` after `opencode-meat`
  is added to `~/.config/opencode/cli.json`.

## Interactive Tests

### 1. Default action

```text
/meat
```

Expected:

- Meat runs immediately without starting an agent turn.
- In this colocated checkout, auto-detect selects jj; source is `@`.
- The synthetic message prefers Meat's `source` label when present.
- It runs for `/home/shuv/repos/meat`, not the service process directory.

### 2. Cached repeat

Run `/meat` again without changing files.

Expected: the same result returns quickly from Meat's cache (no second model
call). Prewarm and foreground share the same cache key.

### 3. Parent change

```text
/meat parent
```

Expected: reading diff for jj `@-` (or Git `HEAD` if `vcs: "git"`).

### 4. Native jj revset

```text
/meat @-
```

Expected: same family of result as parent; invalid revsets surface Meat's error.

### 5. Clean empty UX

In a disposable clean repository (Git-only and pure jj), run `/meat`.

Expected structured empty success, e.g. `# No changes in @.` or
`# No unstaged changes.`, not a failed tool call and not "No staged changes"
wording for jj.

### 6. Staged under jj

```text
/meat staged
```

In a jj-selected repo: actionable error about no staging area (not a silent
Git fallback).

### 7. Force Git in colocated

Configure plugin `vcs: "git"` (server + TUI) and run `/meat` / `/meat staged`.

Expected: Git worktree/index semantics.

## Optional Direct Prompt

Ask the agent:

```text
Run meat on the current change and show me the result.
```

Expected: native `meat` tool with target `current` (or default).

## Failure Signals

- `Could not start ...`: the configured binary path is wrong or inaccessible.
- `update meat`: version skew — plugin expects JSON empty results from a new
  binary.
- `meat failed: ...`: inspect the included Meat/auth/provider error.
- Plugin still mentioning staged/unstaged for jj empty results: outdated plugin.
