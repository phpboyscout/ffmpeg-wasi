---
title: ffmpeg-wasi
description: Current FFmpeg as a sandboxed WASI module — libav-direct, pure-Go-runnable, CGO-free.
---

<div class="hero" markdown>

![ffmpeg-wasi logo](images/branding/logo_transparent.svg)

# ffmpeg-wasi

<p class="hero-tagline">Current FFmpeg as a sandboxed WebAssembly module — for the server, not the browser.</p>

</div>

ffmpeg-wasi builds FFmpeg's `libav*` libraries to `wasm32-wasi` and drives them with a small
purpose-built engine, producing a single `.wasm` that runs media pipelines anywhere a WASI
runtime does — designed for [wazero](https://wazero.io/), the pure-Go runtime, so Go programs
can transcode/filter/mux **embedded, CGO-free, and sandboxed**.

It is the **reference server-side FFmpeg for WebAssembly**: *current* (not the EOL build),
*WASI-native* (not the browser one), and *pure-Go-runnable* (no CGO) — by linking the
libraries directly and going under the FFmpeg-7.0 CLI threading wall that stops everyone else.

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
