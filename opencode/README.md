# opencode-meat

First-class [OpenCode V2](https://opencode.ai/v2/docs/build/plugins) integration for [meat](https://meat.dev).

It adds two integrations:

- A native `meat` tool agents can call when explicitly asked
- A TUI `/meat` action that runs the binary directly, without sending a prompt to an agent

The TUI action accepts `/meat`, `/meat current`, `/meat parent`, `/meat staged`
(Git only), or a Git revision/range or jj revset. Its result is inserted into
the active session as a synthetic message and does not resume the model.

The plugin displays Meat's short plain-language summary before the reading
diff. From the terminal, `meat -summary` prints only that sentence and uses the
same persistent cache as a normal run.

## Install

Install the `meat` binary first:

```sh
go install meat.dev/cmd/meat@latest
```

Add the server plugin to `opencode.json` for the native tool:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugins": ["opencode-meat"]
}
```

Add the TUI entrypoint to `~/.config/opencode/cli.json` for the direct slash action:

```json
{
  "plugins": ["opencode-meat"]
}
```

The server and TUI are separate processes, so each reads its plugin configuration independently. If `cli.json` already contains settings, add the `plugins` field without replacing them.

For local server development from the meat repository, use:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugins": ["./opencode/index.ts"]
}
```

For the local TUI entrypoint, add its absolute path to `~/.config/opencode/cli.json`:

```json
{
  "plugins": ["/absolute/path/to/meat/opencode/tui.ts"]
}
```

Both integrations run meat in the active session's project directory. Meat's normal credentials and cache configuration apply unchanged.

If `meat` is not on either process's `PATH`, configure its absolute path in both files:

```json
{
  "plugins": [
    {
      "package": "opencode-meat",
      "options": {
        "binary": "/home/me/.local/bin/meat",
        "model": "gpt-5.6-luna",
        "vcs": "auto",
        "prewarm": {
          "enabled": true,
          "debounceMs": 1500
        }
      }
    }
  ]
}
```

`model` and `vcs` pin the cache identity and backend used by foreground and
background runs. Set the same options in the TUI plugin entry in `cli.json` so
`/meat` reads the prewarmed result. `vcs` accepts `auto` (default), `git`, or
`jj`; when omitted, Meat auto-detects (colocated repos prefer jj).

Server-side `prewarm` is optional and disabled by default. When enabled,
successful `edit`, `patch`, `write`, and `shell` calls schedule a current-change
reading diff in the background via Meat itself (no plugin-side `git`/`jj`
subprocess). Repeated changes are debounced, obsolete runs are cancelled, and
an idle session starts a pending run immediately. Unchanged diffs hit Meat's
content cache. Prewarm failures are silent; a normal `meat` tool or `/meat`
invocation remains the fallback. On jj, prewarm snapshots the working copy and
can write operation log entries.

## Targets

| Target | Meaning |
| --- | --- |
| `current` (default) | Git worktree / jj `@` |
| `worktree`, `unstaged` | Aliases for `current` |
| `parent` | Git `HEAD` / jj `@-` |
| `staged`, `index` | Git index only (rejected under jj) |
| other non-flag value | Git revision/range or jj revset, passed through |

Do not map `head`/`latest` in the plugin — pass literal `HEAD` when using Git.
