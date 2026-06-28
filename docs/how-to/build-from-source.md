---
title: Build from source
description: Build an ffmpeg-wasi module yourself with the clean-room Docker pipeline.
date: 2026-06-28
tags: [how-to, build]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Build from source

You don't need to build — every release [ships both variants](choose-a-variant.md). But the
whole pipeline is MIT and reproducible, so building it yourself is easy.

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

The module lands at `dist/ffmpeg-wasi-<variant>.wasm`. To build a different upstream FFmpeg,
add `--build-arg FFMPEG_VERSION=n8.1.2` (any FFmpeg release tag).

With [just](https://github.com/casey/just):

```sh
just build lgpl
```

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
ffmpeg-wasi engine (Phase A)
ffmpeg: n8.1.2
libavcodec 4070502  libavformat 4066406  libavfilter 724582
codecs:
  h264       encode:no  decode:yes
  ...
```

## What the build does

Three small scripts under `build/`, orchestrated by `build/Dockerfile`:

1. **`libav.sh`** — clones FFmpeg, configures it single-threaded for `wasm32-wasi`
   (libraries only), and `make`s the `libav*` archives.
2. **`driver.sh`** — links the engine (`src/driver.c`) + the wasi compat shims against those
   archives into one `.wasm` command module.
3. **`toolchain.sh`** — the shared wasi-sdk/clang cross-compile environment.

See [The build](../explanation/the-build.md) for what makes it work (single-threaded config,
setjmp/longjmp lowering, the POSIX/WASI compat shims).
