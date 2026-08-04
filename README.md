# meat

Abridge a code diff into a **reading diff**.

Humans need to review agent-written code in critical systems.
But models are good now. You don't need to review for style or nil-checks
or imports. You need to review concepts, algorithm choices, architecture.

So meat uses a model to reduce a diff to the important parts.
It shows you the meat.

Install with:

```
go install meat.dev/cmd/meat@latest
```

## Repositories

Meat works in Git repositories, pure Jujutsu (`jj`) repositories, and colocated
jj/Git repositories.

Backend selection (first match wins):

1. `-vcs=git|jj`
2. `MEAT_VCS=git|jj`
3. Auto-detect: deepest repository root wins; colocated jj/git prefers jj

```sh
meat                 # git HEAD, or jj @
meat -w              # current change (git worktree / jj @)
meat current         # same as -w
meat parent          # git HEAD / jj @-
meat -staged         # git only
meat HEAD~3          # git revision
meat '@-'            # jj revset (quote for the shell)
meat 'trunk()::@'    # jj revset
git show HEAD | meat # piped unified diff (VCS-neutral)
jj diff --git | meat
```

`current` and `parent` are reserved keywords. To reach a Git branch literally
named `parent`, use `refs/heads/parent`; for a jj bookmark, use
`bookmarks(exact:"parent")`.

jj has no staging area: `-staged` fails under jj (use `-vcs=git` in a colocated
repo if you need the index). jj diff reads snapshot the working copy; background
prewarm can write Meat-visible operation log entries.

## OpenCode

The first-class OpenCode V2 plugin adds a native `meat` tool and `/meat`
command. Install the binary above, then add `"opencode-meat"` to the `plugins`
array in `opencode.json`. See [`opencode/README.md`](opencode/README.md) for
targets, local development, and configuration.

It takes a while to process a commit for reading.
So I suggest you have an agent build `meat` into your devtools so that
it pre-processes it.

Very large diffs are split at file and hunk boundaries and abridged
chunk by chunk (up to a few MB), so one huge commit still produces a
single merged reading diff — it just takes proportionally longer.
