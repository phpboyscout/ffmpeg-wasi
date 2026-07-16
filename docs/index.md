---
title: ffmpeg-wasi
description: Current FFmpeg, libav-direct — a sandboxed WASI module and a native driver. Pure-Go-runnable, CGO-free.
---

<div class="hero" markdown>

![ffmpeg-wasi logo](images/branding/logo_transparent.svg)

# ffmpeg-wasi

<p class="hero-tagline">Current FFmpeg as a sandboxed WebAssembly module — and a native driver for speed. For the server, not the browser.</p>

</div>

ffmpeg-wasi builds FFmpeg's `libav*` libraries and drives them with a small purpose-built engine,
shipping the result as **two runtime targets**: a sandboxed `wasm32-wasi` module that runs media
pipelines anywhere a WASI runtime does — designed for [wazero](https://wazero.io/), the pure-Go
runtime, so Go programs can transcode/filter/mux **embedded, CGO-free, and sandboxed** — and a
**native driver** (real threads + SIMD) that runs the same jobs at **48–58× the software-encode
speed**.

It is the **reference server-side FFmpeg for WebAssembly**: *current* (not the EOL build),
*WASI-native* (not the browser one), and *pure-Go-runnable* (no CGO) — by linking the
libraries directly and going under the FFmpeg-7.0 CLI threading wall that stops everyone else. The
native driver (spec 0028) is the same engine and the same job-spec, built for the host instead of
the sandbox — a drop-in speed tier, driven out-of-process by afmpeg's native backend.

<div class="grid cards" markdown>

- :material-school: **[Tutorials](tutorials/index.md)** — learn by doing.
- :material-wrench: **[How-to](how-to/index.md)** — solve a specific task.
- :material-lightbulb: **[Explanation](explanation/index.md)** — how & why it works.
- :material-book-open-variant: **[Reference](reference/index.md)** — the job-spec vocabulary, artifacts, codec matrix.

</div>

> **Status: released.** Releases ship current FFmpeg (n8.1.2) as **lgpl** and **gpl** builds, in a
> **lean** and an **intermediate** profile — both as portable **WASI modules** and as **native
> drivers** (spec 0028, threads + SIMD, driven by afmpeg's native backend for 48–58× faster software
> encode). The native driver adds a third **full** profile with HEVC (x265) and AV1 (SVT-AV1) encode.
> The engine **transcodes** (decode → filter → encode → mux) over a virtual filesystem: the `probe`,
> `process` (full `filter_complex`), `frames`, and `version` ops all work today. Design: afmpeg
> [spec 0007](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0007-libav-direct-engine.md).
