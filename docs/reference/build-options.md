---
title: Build options
description: Every knob the build takes — the Docker build arguments, the environment variables the scripts read, the pinned dependency versions, and what happens when one is wrong.
date: 2026-08-02
tags: [reference, build]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Build options

The complete set of inputs to a build, what each defaults to, and how it fails when it is wrong.
[Build from source](../how-to/build-from-source.md) is the task-oriented version of this page;
[The build](../explanation/the-build.md) explains why the pipeline is shaped this way.

There are two Dockerfiles and four shell scripts. The Dockerfiles take **build arguments**; the
scripts read **environment variables**. Running the scripts directly (as CI does) means setting the
environment variables yourself.

## Docker build arguments

Both `build/Dockerfile` (the WASM module) and `build/Dockerfile.native` (the native driver) accept
the same three:

| Argument | Default | Accepted | Effect |
|---|---|---|---|
| `VARIANT` | `lgpl` | `lgpl`, `gpl` | `gpl` adds `--enable-gpl` + libx264 (and libx265 in the `full` profile). Sets the artifact's licence — see [licensing](../explanation/licensing.md). |
| `PROFILE` | `lean` | `lean`, `intermediate`, `full` | The capability class. `full` is **native-only**. |
| `FFMPEG_VERSION` | `n8.1.2` | any FFmpeg release tag | Which upstream FFmpeg is cloned and built. |

```sh
docker build -f build/Dockerfile \
  --build-arg VARIANT=gpl --build-arg PROFILE=intermediate \
  --target artifact -o dist .
```

The `--target artifact` stage is a `scratch` image containing only the built binaries, so `-o <dir>`
extracts them without an intermediate container.

### What each build argument produces

Artifact naming is decided in the Dockerfile, and the `lean` profile deliberately keeps the original
unprefixed name so existing consumers are unaffected:

| `PROFILE` | WASM artifact | Native artifact |
|---|---|---|
| `lean` | `ffmpeg-wasi-<variant>.wasm` | `driver` (published as `ffmpeg-wasi-driver-linux-amd64-<variant>`) |
| `intermediate` | `ffmpeg-wasi-intermediate-<variant>.wasm` | `driver` (published as `…-intermediate-<variant>`) |
| `full` | **not built** | `driver` (published as `…-full-<variant>`) |

### What happens when a build argument is wrong

| Mistake | Result |
|---|---|
| `PROFILE=full` on `build/Dockerfile` (the WASM one) | `build/libav.sh` exits `2`: `PROFILE=full is native-only` |
| `PROFILE` set to anything else | `build/libav.sh` exits `2`: `unknown PROFILE '<x>'`, naming the three it accepts |
| `VARIANT` set to anything but `lgpl`/`gpl` | `build/deps.sh` exits `2`: `deps: unknown VARIANT <x>` |
| `FFMPEG_VERSION` naming a tag that does not exist | the `git clone --branch` in `build/libav.sh` fails |
| `FFMPEG_VERSION` unset when running `build/libav.sh` directly | the script aborts: `set FFMPEG_VERSION, e.g. n8.1.2` |

`VARIANT` is validated in `deps.sh` only. `libav.sh` treats anything other than `gpl` as the LGPL
path, so a typo reaching `libav.sh` alone would silently build an LGPL artifact — always run
`deps.sh` first, as both Dockerfiles and CI do.

## Environment variables the build scripts read

`build/toolchain.sh` is sourced by the other three and establishes the compile environment.

| Variable | Default | Read by | Purpose |
|---|---|---|---|
| `TARGET` | `wasm` | all four | `wasm` selects the wasi-sdk cross toolchain; `native` selects the host toolchain with threads, SIMD and asm. `build/Dockerfile.native` sets it. |
| `PREFIX` | `/opt/vendor` | all four | Where the external codec libraries install, and where `driver.sh` looks for them at link time. |
| `WASI_SDK` | `/opt/wasi-sdk` | `toolchain.sh` (wasm only) | The wasi-sdk root; its `share/wasi-sysroot` becomes `WASI_SYSROOT`. |
| `CC` / `CXX` | `cc` / `c++` (native), `clang` / `clang++` (wasm) | `toolchain.sh` | The compilers. Overridable on native only; the wasm path pins clang from the SDK. |
| `FFMPEG_VERSION` | *(required)* | `libav.sh` | The upstream tag to clone. |
| `VARIANT` | `lgpl` | `deps.sh`, `libav.sh` | The licence variant. |
| `PROFILE` | `lean` | `deps.sh`, `libav.sh` | The capability profile. |
| `FFMPEG_SRC` | `/ffmpeg` | `libav.sh`, `driver.sh` | Where FFmpeg is cloned to and where `driver.sh` finds the built `libav*` archives. |
| `SRC_DIR` | `<repo>/src` | `driver.sh` | The engine sources. |
| `DRIVER_SRC` | `$SRC_DIR/driver.c` | `driver.sh` | The engine entry point. |
| `OUT` | `/dist/ffmpeg-wasi.wasm` | `driver.sh` | The output path for the linked binary. Both Dockerfiles set it to the profile-correct name. |

Running the scripts by hand is the same three steps CI takes:

```sh
export FFMPEG_VERSION=n8.1.2 VARIANT=lgpl PROFILE=lean
sh build/deps.sh
sh build/libav.sh
OUT=dist/ffmpeg-wasi-lgpl.wasm sh build/driver.sh
```

## The `just` recipes

| Recipe | Does |
|---|---|
| `just build <variant> <profile>` | The WASM Docker build. Defaults `lgpl lean`. This is the default recipe. |
| `just run <variant>` | Runs `dist/ffmpeg-wasi-<variant>.wasm` under the bundled wazero harness and prints the capability report. Defaults `lgpl`. |
| `just lint` | `shellcheck build/*.sh` — the same check CI runs on a merge request. |
| `just docs-serve` | Serves this documentation site locally with `zensical serve`. |

`just build` covers `lean` and `intermediate` only. The native driver has no recipe; invoke
`build/Dockerfile.native` directly.

## Pinned dependency versions

Every external library is pinned in `build/deps.sh` and overridable through the environment. The
licence shown is the one `deps.sh` records for it.

| Variable | Default | Licence | Profile | For |
|---|---|---|---|---|
| `OPENH264_VERSION` | `v2.6.0` | BSD-2-Clause | all | H.264 encode, both variants |
| `X264_COMMIT` | `b35605ac…` | GPL-2.0+ | all | H.264 encode, **gpl only** |
| `ZLIB_VERSION` | `v1.3.1` | Zlib | all (WASM) | FFmpeg's native PNG codec |
| `OPUS_VERSION` | `1.5.2` | BSD | intermediate, full | Opus encode |
| `LAME_VERSION` | `3.100` | LGPL | intermediate, full | MP3 encode |
| `OGG_VERSION` | `1.3.5` | BSD | intermediate, full | Vorbis container dependency |
| `VORBIS_VERSION` | `1.3.7` | BSD | intermediate, full | Vorbis encode |
| `WEBP_VERSION` | `1.4.0` | BSD | intermediate, full | WebP encode |
| `VPX_VERSION` | `v1.14.1` | BSD | intermediate, full | VP8/VP9 encode |
| `FREETYPE_VERSION` | `2.13.3` | FTL | intermediate, full | glyph rasteriser for `drawtext`/`subtitles` |
| `HARFBUZZ_VERSION` | `8.5.0` | MIT | intermediate, full | text shaping; FFmpeg 8's `drawtext` requires it |
| `FRIBIDI_VERSION` | `1.0.16` | LGPL | intermediate, full | bidirectional text; libass requires it |
| `LIBASS_VERSION` | `0.17.3` | ISC | intermediate, full | `subtitles`/`ass` burn-in |
| `DAV1D_VERSION` | `1.5.0` | BSD | intermediate, full | AV1 **decode**, both runtimes |
| `SVTAV1_VERSION` | `v2.3.0` | BSD-3-Clause-Clear | full (native) | AV1 encode, both variants |
| `X265_VERSION` | `3.6` | GPL-2.0+ | full (native) | HEVC encode, **gpl only** |

x264 is pinned by **commit**, not tag, because x264 publishes no release tags. Bump `X264_COMMIT` to
advance it.

### Tarball digests are pinned alongside the version

The libraries fetched as release tarballs — Opus, LAME, Ogg, Vorbis, WebP, FreeType, HarfBuzz,
FriBidi and libass — are verified against a **SHA-256 hard-coded in `build/deps.sh`**, so a
compromised or altered mirror cannot slip modified source into an artifact. Those digests are *not*
environment-overridable.

**Overriding the version without editing the digest fails the build.** `fetch_tarball` tries each
mirror, rejects every download whose digest does not match, and then aborts with
`fetch: all mirrors failed (or checksum mismatched)`. Bump the version constant and its
`*_SHA256` constant in the same edit.

The git-cloned libraries — openh264, x264, x265, SVT-AV1, dav1d, zlib and FFmpeg itself — carry no
digest and are pinned by tag or commit.

## `build/versions.lock` is a record, not an input

`build/versions.lock` documents the pinned FFmpeg tag, the wasi-sdk image and several dependency
versions in a shell-assignment format. **No script sources it.** The real values live in three
places:

- the base image line and `ARG` defaults in each Dockerfile (the wasi-sdk image, `FFMPEG_VERSION`);
- the `: "${VAR:=default}"` defaults in `build/deps.sh` (every codec library);
- the release tag in CI, which sets `FFMPEG_VERSION` from `$CI_COMMIT_TAG` with the build revision
  stripped (`n8.1.2-10` → `n8.1.2`).

Treat it as a summary to keep in step, and change the authoritative file as well.

## The size-budget gate

`build/size-budget.txt` sets a per-artifact byte ceiling; `build/check-size-budget.sh` compares each
built artifact against its line and prints the result in the `size-budget` CI job on every tag. It
is a tripwire for a profile ballooning, not a drift alarm, and the ceilings are deliberately
generous. The job is **advisory** (`allow_failure`) until the ceilings are calibrated against real
builds — an overage is reported, not enforced.

## Related

- [Build from source](../how-to/build-from-source.md) — the recipes, step by step.
- [The build](../explanation/the-build.md) — why the toolchain, the shims and the two targets exist.
- [Variants & artifacts](variants.md) — what each combination produces and how it is published.
