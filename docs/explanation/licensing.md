---
title: The licensing model
description: Why the tooling is MIT, the artifacts are LGPL or GPL, and shipping both is clean.
date: 2026-06-28
tags: [explanation, licensing]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# The licensing model

Media tooling licensing is where most projects get vague. ffmpeg-wasi keeps three licences
**deliberately distinct**, so you always know exactly what you're holding.

!!! note "Not legal advice"
    This explains the project's intent and structure. It is not legal advice; a formal
    licence review precedes any tagged release.

## Three licences, kept apart

### 1. This repository's source — **MIT**

The build tooling (`build/`) and the engine (`src/driver.c`) are [MIT](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/blob/main/LICENSE).
Two facts keep it that way:

- **It vendors no FFmpeg source.** FFmpeg (and any GPL dependency such as x264) is *cloned
  at build time*, never committed here.
- **It links nothing.** The tooling *orchestrates* a build — clone, configure, compile,
  package. It is not a derivative work of anything GPL, so it stays MIT, and it is yours to
  reuse.

That MIT pipeline — the reference "FFmpeg → WASI, libav-direct" build — is the valuable,
reusable part we own.

### 2. The released artifacts — **LGPL or GPL** (your choice)

FFmpeg's libraries are **LGPL-2.1-or-later** by default. The artifact's licence is set
entirely by *what we enable*:

| Variant | Built with | Artifact licence |
|---|---|---|
| **LGPL** (default) | stock `libav*`, no `--enable-gpl` | LGPL-2.1-or-later |
| **GPL** (opt-in) | `--enable-gpl` + libx264 | GPL-2.0-or-later |

**LGPL is the floor.** We cannot relicense `libav*` below it — it's the FFmpeg project's
code, not ours. But LGPL is proprietary-compatible (you may use it in closed software,
subject to its relink provision), which is what matters for adoption.

### 3. Your code that *consumes* the artifact — **unaffected**

[afmpeg](https://gitlab.com/phpboyscout/afmpeg), or whatever loads the module, just downloads
a `.wasm`. The LGPL/GPL obligation attaches to *that artifact* and whoever distributes it —
never to the consuming source. afmpeg stays permissive throughout.

## Why shipping both variants is clean

Every release publishes **both** `ffmpeg-wasi-lgpl.wasm` and `ffmpeg-wasi-gpl.wasm`, so you
pick the licence that fits and never have to build. Distributing two separate, independent
artifacts together is **mere aggregation** (GPLv3 §5): the GPL artifact does not "infect"
the LGPL artifact, the MIT tooling, or your code. They simply coexist.

## Obligations we meet

- **Per-asset labelling** — each artifact states its licence; the provenance manifest records
  variant + licence.
- **Corresponding source** — for every released binary, the source is the *pinned upstream
  FFmpeg/x264* plus *this public repository* (our scripts + engine). Anyone can rebuild.
- **LGPL relink** — because the build is public and the FFmpeg version pinned, a user can
  relink the LGPL artifact against a modified `libav*`.

## In one sentence

**MIT is what we own; LGPL is the floor for the artifact; GPL is opt-in for libx264; and your
code stays yours.**
