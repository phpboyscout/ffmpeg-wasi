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

### What it deliberately does not do

- **It does not ship the `ffmpeg` command line.** FFmpeg 7.0+ made the CLI
  multithreaded and a pure-Go WASI runtime cannot spawn threads, so this repo
  links `libav*` directly and drives it with an engine written here: the six
  files in `src/`, under 3,000 lines of C plus vendored cJSON. There is no
  argv-compatible surface and no plan to grow one.
- **It answers a JSON job spec, not a command line.** One argument, dispatched
  on `"op"` in `src/driver.c`: `probe`, `process`, `frames`, `version`, plus the
  `--capabilities` flag. A spec whose `version` is newer than the engine's
  vocabulary is refused rather than half-executed.
- **The Go here is not a library.** `internal/` is the conformance harness, and
  `internal/ipchost` is a second, independent implementation of the IPC host
  written so that an engine bug cannot hide behind a host bug (spec 0037 D4).
  `tools/run` is a local smoke runner. Consumers reach the engine through
  afmpeg and its published artefacts, never by importing this module.
- **It resolves no font by name.** fontconfig is absent, so `drawtext` takes
  `fontfile` and libass gets whatever files it was handed. Whether that should
  change is #37, still open and deliberately framed as a decision rather than a
  fix.
- **The native drivers are not a hardware path.** They exist for real threads
  and SIMD. There is no GPU or hwaccel story here.

**The specs this repo is built to are not in this repo.** The numbers that
appear all through `src/` and `internal/` (0022 profiles, 0028 native drivers,
0035 the release pipeline, 0036 the conformance suite, 0037 parity) are
afmpeg's, split between its wiki and its `docs/development/specs/`. There is
nowhere local to look them up.

## Where it has got to

**The build is the settled part.** Ten artefacts per release: `wasm` in lean and
intermediate, `native` in lean, intermediate and full, each in `lgpl` and `gpl`.
The naming scheme is load-bearing rather than cosmetic, because
`internal/engine/artifact.go` recovers `(target, profile, variant)` from the
filename instead of trusting an environment variable. `build/ffmpeg-version.txt`
is authoritative for the FFmpeg version, and a tag that disagrees with it fails
in `validate` before an hour of compiling starts. Thirteen tags so far, all
shaped `nX.Y.Z-N`; `n9.0.1-1` is current. A release is cut by pushing such a
tag, not by a commit type.

**The engine is the moving part**, and it is moving a great deal. Around fifty
issues in the #11 to #60 range are open, nearly all engine defects, and they
were all raised between 19 and 21 August 2026: one three-day wave that started
when someone re-measured the n9.0.1 bump and kept pulling. Two architecture
reviews sorted that population into two families; #60 argues for a third.
Reading a handful of them is the fastest way to see what this code is like:

- **Swallowed failure.** A return value discarded, so a job that failed exits 0
  with a plausible file. The biggest family by some margin: #15, #16, #20, #28,
  #38, #43, #45, #46, #53.
- **Escape below the seam.** A path that reaches the host filesystem without
  crossing the IPC bridge, against the no-host-disk guarantee
  `docs/reference/driver-invocation-abi.md` makes: #13, #14, #36, #48.
- **Liveness.** A job that neither escapes nor lies, and never terminates. #19,
  #58, #31. It has already recurred once through its own fix, which is the
  argument #60 makes for treating it as a family rather than a stray.
- **Protocol gaps no patch closes.** The IPC frame set cannot express a read
  failure (#32) or a rename (#35), so both want a version bump rather than a
  fix. #47 and #52 are the same shape one level down.
- **A value taken at face value instead of being range-checked or refused.**
  #23, #24, #25, #42, #44, #50, #56.
- **Lane asymmetry.** The engine has three lanes (filter graph, stream copy,
  subtitle) and the same job spec behaves differently depending on which one it
  takes: #18, #21, #40, #41, #54, #59.

**MR !99 is the other half of the picture.** It fixes thirty-seven of these
defects across nineteen tickets, and it is not merged. It touches `src/`, `build/`, `internal/conformance/`,
`internal/fixture/` and `internal/engine/`, so anything in those directories is
about to move under you.

**The docs lag the engine.** #34 lists ten files still presenting `n8.1.2` as
current, including a pinned download URL and SHA-256 in
`docs/how-to/choose-a-variant.md` that point at a superseded release.

**The published speed figure is unsettled.** `48-58x` appears in the README,
five docs pages and the release description in `.gitlab-ci.yml`.
Per issue #9 it is the ratio of the WASM module to our own native driver at
320x240, from the spec 0028 spike, and a note on #9 dated 2026-08-21 records an
n9.0.1 run measuring 46.1x on the same comparison, so it is not contradicted.
What #9 still wants is a measurement at a realistic resolution, both licence
variants, and an honest statement that the number is workload-dependent. It is
open and its deliverable is unmet. Do not quote the figure as re-established,
and do not repeat the "7.5x to 280x" spread as though it were one quantity: the
endpoints are three different comparisons.

## The traps

**A stale artefact matrix reads exactly like a passing one.** A whole session
was spent carrying a green baseline across ten artefacts that had been built
three commits earlier, so one fix in that window was never exercised at all.
Nothing in the output says how old a binary is. The cheap check is to pick a
string the newest fix introduced and `strings <artefact> | grep` for it across
all ten. Better still, rebuild: the deps and `libav*` layers are cached
separately from `src/` in both Dockerfiles, so relinking the engine is minutes
even though a cold build is a two-hour job.

**A frame count and a duration catch different defects, and neither catches
both.** #12 was a frame short at every fixture length and the duration check
could not see it. #59 is the mirror image: every packet is present and only the
container's reported duration is wrong, so counting frames cannot see it, and it
was found because a duration assertion went red against a copy that was
demonstrably complete. #58 sits on top of both, because its companion case,
proving a stop condition does not cut the copy lane short, has to count frames:
#59 has made duration useless on exactly that lane. A test asserting only one of
the two instruments is blind to the other class.

Do not read that as "a count is sufficient for #12". #12 has two symptoms and
they need different instruments. On a uniform sequence through the passthrough
lane, count and duration move together: 24 frames where 25 were expected, and
0.96s where 1.0s was. Under xfade, with the output not ending on a frame
boundary, all 48 frames are present and only the container disagrees, reporting
1.566667s against 1.600000s. The same ticket is a count defect on one lane and a
duration defect on another.

**A tolerance wider than the defect is a check that cannot fail.**
`durationTolerance` in `internal/conformance/behaviour_test.go` is 0.2s. The #12
defect is one frame: 0.04s at 25fps, 0.033s at 30fps. That check ran and passed
for the entire life of the project and could not have failed. It is why nearly
every ticket here carries the line about asserting the thing that is wrong
rather than something correlated with it.

**Prove a guard can fail before you trust it.** For #58 a deliberately wrong
stop condition was written, one that ignored the copy lane, purely to confirm
the companion test went red under it. Twice in one week a test here turned out
to be non-discriminating, and only sabotaging the code revealed it.

**A ratio below 1 is the only signal that a benchmark run is invalid.** One run
in the #9 set is filed CONTAMINATED: it used the host's system ffmpeg 6.1.1
instead of an n9.0.1 build, under CPU contention, and produced a 0.4x row.
Nothing else in the report flags it. A contaminated run reads exactly like a
valid one until you notice that WASM has apparently beaten native.

**The issue list is a record of what has been written down, not of what is
broken.** It is wrong in both directions. It over-counts: !99's description
lists nineteen of the currently-open issues as already fixed on its branch,
thirty-seven defects in all, so read the branch before reproducing anything or
you will fix something twice. It also under-counts: #58 and #59 were both found
and fixed in a single session, and neither existed as a ticket that morning.
Treat it as a starting point for what is known, never as an inventory of what is
wrong.

**`go test ./...` on its own proves very little.** Without
`FFMPEG_WASI_ARTIFACTS` naming a directory of built engines, every
artefact-backed test skips and the run still says ok. `Discover()` also ignores
any filename it cannot parse rather than erroring, so a mistyped or half-copied
artefact is silently absent from the matrix instead of failing. And the `/dev`
entries `internal/engine/workspace.go` creates are ordinary files, not devices,
so passing here is not evidence that a host serving real ones behaves the same.

**Both lanes agreeing is not both lanes being right.** The parity layer (spec
0037) compares what two artefacts answer for the same job. A fault the WASM and
native builds share is invisible to it by construction, which is exactly how #11
and #12 survived it. Recorded on the ticket as 0037 D9.

**The native driver only sandboxes itself when it is asked to.** With
`AFMPEG_NATIVE_SOCKET` unset, the native build is an ordinary program opening
ordinary host paths. That is documented and intended, and it means a local
reproduction run straight from a shell is not exercising the bridge at all.

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
- The build matrix is serialised behind one project-scoped `resource_group`, so
  a full pipeline is around 95 minutes rather than 17. That is a deliberate
  trade for the shared runners, explained at the top of `.gitlab-ci.yml`. Do not
  remove it to make a release faster.
