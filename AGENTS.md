# AGENTS.md

This file provides guidance to AI coding agents (Claude Code, agy, codex, etc.) when working with code in this repository.

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

It does not ship the `ffmpeg` command line, and will not grow one: FFmpeg 7.0+
made the CLI multithreaded and a pure-Go WASI runtime cannot spawn threads, so
this repo links `libav*` directly and drives it with its own engine, the six
files in `src/`. That engine answers a JSON job spec rather than an argv,
dispatched on `"op"` in `src/driver.c`. The Go here is not a library either:
`internal/` is the conformance harness and `tools/run` a smoke runner, and
consumers reach the engine through afmpeg's published artefacts rather than by
importing this module. There is no font resolution (fontconfig is absent) and no
GPU or hwaccel path.

**The specs this repo is built to are not in this repo.** The numbers that
appear all through `src/` and `internal/` are afmpeg's, split between its wiki
and its `docs/development/specs/`. There is nowhere local to look them up.

## Where it has got to

**The build is the settled part.** Ten artefacts per release, `wasm` and
`native` across the profile and licence variants. The naming scheme is
load-bearing rather than cosmetic, because `internal/engine/artifact.go`
recovers `(target, profile, variant)` from the filename instead of trusting an
environment variable. A release is cut by pushing an `nX.Y.Z-N` tag, not by a
commit type.

**The engine is the moving part**, and moving a great deal. Around fifty issues
in the #11 to #60 range are open, nearly all engine defects, almost all raised
in a single three-day wave. MR !99 is the other half of that picture: it fixes
thirty-seven of them and is not merged, and it touches `src/`, `build/`,
`internal/conformance/`, `internal/fixture/` and `internal/engine/`, so anything
in those directories is about to move under you. Read that branch before
reproducing a defect, and treat the issue list as a record of what has been
written down rather than an inventory of what is broken.

## The traps

**A stale artefact matrix reads exactly like a passing one.** A whole session
was spent carrying a green baseline across ten artefacts that had been built
three commits earlier, so one fix in that window was never exercised at all.
Nothing in the output says how old a binary is. The cheap check is to pick a
string the newest fix introduced and `strings <artefact> | grep` for it across
all ten. Better still, rebuild: the deps and `libav*` layers are cached
separately from `src/` in both Dockerfiles, so relinking the engine is minutes
even though a cold build is a two-hour job.

**`go test ./...` on its own proves very little.** Without
`FFMPEG_WASI_ARTIFACTS` naming a directory of built engines, every
artefact-backed test skips and the run still says ok. `Discover()` also ignores
any filename it cannot parse rather than erroring, so a mistyped or half-copied
artefact is silently absent from the matrix instead of failing. And the `/dev`
entries `internal/engine/workspace.go` creates are ordinary files, not devices,
so passing here is not evidence that a host serving real ones behaves the same.

**Both lanes agreeing is not both lanes being right.** The parity layer compares
what two artefacts answer for the same job, so a fault the WASM and native
builds share is invisible to it by construction. That is exactly how #11 and #12
survived it.

## Which skills apply here

| When | Skill |
|---|---|
| Picking up one of the open engine issues | `triage-an-issue` |
| Reproducing a defect before you touch the fix | `diagnose-with-a-red-loop` |
| Writing or judging a conformance test | `test-first-discipline` |
| Trusting a build, a green run or a benchmark you did not just produce | `verify-dont-trust` |
| Writing anything others will read and check | `checkable-claims` |
| Editing anything under `docs/` | `diataxis-docs` |
| Landing a change through a pipeline this long | `drive-ci-from-the-cli` |
| A fix that really belongs in afmpeg | `raise-a-forge-issue` |
| Writing a commit message or a merge request description | `conventional-commits` |
| Committing, branching, merging, or opening a merge request | `forge-publish-workflow` |
| Working in a repo other than the one you were invoked in | `cross-repo-worktree` |

> Skills are a Claude Code mechanism, shipped by the
> [phpboyscout marketplace](https://gitlab.com/phpboyscout/claude-code-plugins).
> An agent without them should treat a named skill as a topic to ask about
> rather than a file it can load.

## House rules

- Linear history. Rebase and fast-forward; never squash-merge from the UI.
- Conventional Commits, but here the type does not cut a release. This project
  has no releaser-pleaser and no version-from-commits machinery: the `release`
  job runs only on a tag matching `^n[0-9]`, so pushing an `nX.Y.Z-N` tag is the
  one thing that cuts a release.
- Never cut a release yourself. That is the maintainer's call, every time.
- No AI attribution in anything published, and never at-mention anyone.
- The build matrix is serialised behind one project-scoped `resource_group`, a
  deliberate trade for the shared runners explained at the top of
  `.gitlab-ci.yml`. Do not remove it to make a release faster.
