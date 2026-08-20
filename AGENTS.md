# AGENTS.md

This file provides guidance to AI coding agents (Claude Code, agy, codex, etc.) when working with code in this repository.

**This file is a seed.** It carries what could be derived from the repository
and checked. What this is really for, where it has got to, and the traps it sets
are not here yet. Issue #57 tracks filling that in.

Ways of working live in the phpboyscout skills and are not repeated here, since
naming a skill ages better than restating it.

## What this is

`gitlab.com/phpboyscout/ffmpeg-wasi` builds FFmpeg's media libraries to
`wasm32-wasi`, producing a sandboxed WebAssembly module for server-side use
rather than the browser.

**It pairs with [`afmpeg`](https://gitlab.com/phpboyscout/afmpeg)**, the pure-Go
binding that executes the module this repo produces. The pairing is real but is
not a Go module dependency in either direction, so neither `go.mod` shows it and
a change here can break afmpeg quietly.

It has no phpboyscout toolkit dependencies, and its `justfile` defines `lint`
but no aggregate `ci` target, unlike most Go repos here.

## Which skills apply here

| When | Skill |
|---|---|
| Writing anything others will read and check | `checkable-claims` |
| Reaching for a dependency the toolkit may already have | `use-the-go-toolkit` |
| Writing a commit message or a merge request description | `conventional-commits`, `pre-1-0-release-safety` |
| Committing, branching, merging, or opening a merge request | `forge-publish-workflow` |
| Working in a repo other than the one you were invoked in | `cross-repo-worktree` |

> Skills are a Claude Code mechanism, shipped by the
> [phpboyscout marketplace](https://gitlab.com/phpboyscout/claude-code-plugins).
> An agent without them should treat a named skill as a topic to ask about
> rather than a file it can load.

## House rules

- Linear history. Rebase and fast-forward; never squash-merge from the UI.
- Conventional Commits, and the type decides whether a release is cut. Only
  `feat` and `fix` release.
- No AI attribution in anything published, and never at-mention anyone.
- Never cut a release yourself. That is the maintainer's call, every time.

