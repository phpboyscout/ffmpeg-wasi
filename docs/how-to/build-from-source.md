---
title: Build from source
description: Build an ffmpeg-wasi module yourself with the clean-room Docker pipeline.
date: 2026-06-28
tags: [how-to, build]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Build from source

You don't need to build — every release [ships both variants across the profiles](choose-a-variant.md),
as WASM modules and native drivers. But the whole pipeline is MIT and reproducible, so building it
yourself is easy.

## Prerequisites

- **Docker** (the build runs in a `wasi-sdk` image; nothing is installed on your host).
- ~2 GB of disk and a few minutes.

## Build a variant

=== "LGPL (default)"

    ```sh
    docker build -f build/Dockerfile \
      --build-arg VARIANT=lgpl \
      --target artifact -o dist .
    ```

=== "GPL (libx264)"

    ```sh
    docker build -f build/Dockerfile \
      --build-arg VARIANT=gpl \
      --target artifact -o dist .
    ```

The module lands at `dist/ffmpeg-wasi-<variant>.wasm`. The upstream FFmpeg it builds comes from
`build/ffmpeg-version.txt`; to try a different one for a one-off experiment, add
`--build-arg FFMPEG_VERSION=n9.0.1` (any FFmpeg release tag). Changing it for real means editing
that file, which is what CI and both Dockerfiles read.

### Build the intermediate profile

Add `--build-arg PROFILE=intermediate` (default `lean`) to build the full software-codec
module — the LGPL encoders, the native codec/container batches, and text/subtitle burn-in:

```sh
docker build -f build/Dockerfile \
  --build-arg VARIANT=lgpl --build-arg PROFILE=intermediate \
  --target artifact -o dist .
```

It lands at `dist/ffmpeg-wasi-intermediate-<variant>.wasm` — the same asset the release
publishes. See [variants & profiles](../reference/variants.md#profiles-capability-classes).

With [just](https://github.com/casey/just):

```sh
just build lgpl              # lean (default)
just build lgpl intermediate # intermediate profile
```

## Build the native driver (Backend B)

The same engine also builds to a **native ELF** (spec 0028) via `build/Dockerfile.native` — the
subprocess afmpeg's [native backend](https://afmpeg.phpboyscout.uk/how-to/use-the-native-backend/)
drives for native-speed encode. It takes the same `VARIANT`/`PROFILE` args, and adds a third
profile, **`full`** (native-only): intermediate + AV1 (SVT-AV1, both variants) and HEVC (x265,
**gpl only**).

```sh
docker build -f build/Dockerfile.native \
  --build-arg VARIANT=gpl --build-arg PROFILE=full \
  --target artifact -o dist-native .
```

It lands at `dist-native/driver`, published as `ffmpeg-wasi-driver-linux-amd64-full-gpl`. Use
`PROFILE=lean` or `intermediate` for the lighter native drivers; `lgpl` full builds AV1 but not
HEVC (x265 is GPL). linux/amd64 only for now.

## Run it

The repo bundles a tiny wazero harness that loads the module and runs it (it provides the
`env` setjmp/longjmp imports and the WebAssembly feature set the build needs):

```sh
go run ./tools/run dist/ffmpeg-wasi-lgpl.wasm
# or: just run lgpl
```

You'll see the engine's capability report — the FFmpeg version and the available
codecs/muxers/filters — confirming it links and runs:

```
ffmpeg-wasi engine
vocab_version: 9
ffmpeg: n8.1.2
libavcodec 4070502  libavformat 4066406  libavfilter 724582
encoders:
  libopenh264 yes
  libx264     no          # gpl variant only
  mjpeg       yes
  aac         yes
  ...
decoders:
  h264       yes
  ...
```

The report probes a fixed handful of codecs — `libopenh264`, `libx264`, `mjpeg`, `aac`, `flac` and
`pcm_s16le` for encode; `h264`, `hevc`, `vp9`, `aac`, `mp3`, `opus` and `flac` for decode. It is a
build smoke test, not an inventory: for the full set see [codecs](../reference/codecs.md).

## What the build does

Four small scripts under `build/`, orchestrated by `build/Dockerfile`:

1. **`deps.sh`** — clones/cross-compiles the external codec libraries into `$PREFIX`: openh264
   (+ libx264 on gpl), and for the intermediate/full profiles Opus/MP3/Vorbis/WebP/VP8-9,
   freetype/harfbuzz/libass, **AV1 decode (libdav1d)**, and — native full only — x265 / SVT-AV1.
2. **`libav.sh`** — clones FFmpeg, configures it single-threaded for `wasm32-wasi`
   (libraries only), and `make`s the `libav*` archives.
3. **`driver.sh`** — links the engine (`src/driver.c`) + the wasi compat shims against those
   archives into one `.wasm` command module.
4. **`toolchain.sh`** — the shared wasi-sdk/clang cross-compile environment.

All four branch on `TARGET` (`wasm` by default, `native` for the driver), so one build system
produces both artifacts. See [The build](../explanation/the-build.md) for what makes it work
(single-threaded config, setjmp/longjmp lowering, the POSIX/WASI compat shims) and
[The native driver](../explanation/the-build.md#the-native-driver-spec-0028) for the `TARGET=native`
path.
