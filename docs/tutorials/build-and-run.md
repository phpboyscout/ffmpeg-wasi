---
title: Build ffmpeg-wasi and run it
description: A first end-to-end pass — build the module from source and run current FFmpeg under a pure-Go runtime.
date: 2026-06-28
tags: [tutorial]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Build ffmpeg-wasi and run it

In a few minutes you'll build current FFmpeg into a single WebAssembly module and watch it
run under a **pure-Go** runtime — no native FFmpeg, no CGO, sandboxed. This is the "it really
works" tour.

## What you need

- **Docker** and **Go** installed.
- A clone of the repo:

  ```sh
  git clone https://gitlab.com/phpboyscout/ffmpeg-wasi
  cd ffmpeg-wasi
  ```

## Step 1 — build the module

```sh
docker build -f build/Dockerfile --build-arg VARIANT=lgpl --target artifact -o dist .
```

Docker pulls the `wasi-sdk` image, clones FFmpeg, compiles the `libav*` libraries to
`wasm32-wasi`, and links them with the engine. When it finishes:

```sh
ls -lh dist/
# ffmpeg-wasi-lgpl.wasm   ~4.7M
```

That single file **is** FFmpeg's media stack, compiled to WebAssembly.

## Step 2 — run it under wazero

The repo includes a small [wazero](https://wazero.io/) harness — a *pure-Go* WebAssembly
runtime. Run the module through it:

```sh
go run ./tools/run dist/ffmpeg-wasi-lgpl.wasm
```

You'll see the engine report itself:

```
ffmpeg-wasi engine (Phase A)
ffmpeg: n8.1.2
libavcodec 4070502  libavformat 4066406  libavfilter 724582
codecs:
  h264       encode:no  decode:yes
  aac        encode:yes decode:yes
  ...
muxers:
  mp4        yes
  ...
filters:
  scale      yes
  ...
```

## What just happened

- You compiled **current FFmpeg (n8.1.2)** — not an end-of-life pin — to a portable `.wasm`.
- It ran under a **pure-Go runtime**: that harness cross-compiles to a single static binary
  with no CGO and no native FFmpeg anywhere.
- Everything executed inside the **WebAssembly sandbox**.

The harness in `tools/run` does the same two things any host must: provide the `env`
setjmp/longjmp imports and enable the right WebAssembly features (see
[The build](../explanation/the-build.md)). In real use, [afmpeg](https://gitlab.com/phpboyscout/afmpeg)
does this for you and bridges the filesystem so you can run media jobs entirely in memory.

## Next

- Understand the design → [Why libav-direct](../explanation/why-libav-direct.md)
- Pick a release artifact instead of building → [Choose & verify a variant](../how-to/choose-a-variant.md)
- The operations the engine will accept → [The job-spec vocabulary](../reference/job-spec.md)

!!! note "Transcoding"
    The capability report is today's Phase-A engine. The full decode→filter→encode→mux job
    (a real in-memory transcode) lands with the Phase-B engine; this tutorial gains that step
    then.
