---
title: ffmpeg-wasi
description: "Current FFmpeg, libav-direct: a sandboxed WASI module and a native driver. Pure-Go-runnable, CGO-free."
tags: [overview, introduction, ffmpeg, wasm, wasi]
hide:
  - navigation
---

<div class="hero">
  <div class="hero-mark">
    <img src="images/branding/logo_transparent.svg" alt="">
  </div>
  <div class="hero-body">
    <p class="hero-eyebrow">current ffmpeg · wasi · no cgo</p>
    <h1 class="hero-title">ffmpeg-wasi</h1>
    <p class="hero-tagline">Every FFmpeg-in-WebAssembly hit the same wall. This one went under it.</p>
    <p class="hero-description">
      Current FFmpeg as a sandboxed WebAssembly module for the server, plus a
      native driver for speed. It links the <code>libav*</code> libraries
      directly and drives them with its own small engine, so a Go program can
      transcode, filter and mux media embedded, CGO-free and sandboxed under
      <a href="https://wazero.io/">wazero</a>, and the same jobs run at native
      speed when the sandbox is not the point.
    </p>
    <div class="install-box">
      <span class="install-command">docker build -f build/Dockerfile --build-arg VARIANT=lgpl --target artifact -o dist .</span>
      <button class="install-copy" type="button" title="Copy to clipboard">copy</button>
    </div>
    <div class="hero-buttons">
      <a href="tutorials/build-and-run/" class="btn btn-primary">Build and run it</a>
      <a href="https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases" class="btn btn-secondary">Download a release</a>
    </div>
  </div>
</div>

<div class="cap-grid">
  <div class="cap">
    <h3>Current, not EOL</h3>
    <p>Tracks current FFmpeg (n9.0.1 today) rather than the 5.1 that every other WASI build pinned. For a library whose whole job is parsing untrusted media, the security backports are the point.</p>
  </div>
  <div class="cap">
    <h3>Under the threading wall</h3>
    <p>FFmpeg 7 made its command-line tool multithreaded, which a pure-Go runtime cannot run. ffmpeg-wasi skips the CLI, links the libraries single-threaded, and drives them with its own engine.</p>
  </div>
  <div class="cap">
    <h3>Sandboxed and CGO-free</h3>
    <p>One <code>.wasm</code> module that runs anywhere a WASI runtime does. Built for wazero, so a Go binary embeds it, cross-compiles to a single static file, and never shells out.</p>
  </div>
  <div class="cap">
    <h3>A native driver, same engine</h3>
    <p>The same engine and the same job spec, built for the host with real threads and SIMD. Roughly 50× (openh264) to 170× (libx264) the sandboxed software-encode speed, driven out-of-process by afmpeg.</p>
  </div>
</div>

## Where to go

<div class="grid cards" markdown>

- :material-school: **[Tutorials](tutorials/index.md)**: learn by doing.
- :material-wrench: **[How-to](how-to/index.md)**: solve a specific task.
- :material-lightbulb: **[Explanation](explanation/index.md)**: how it works, and why.
- :material-book-open-variant: **[Reference](reference/index.md)**: the job-spec vocabulary, build options, codec matrix, errors.

</div>

**Before you start, check it does what you need.**
[Limits & what is not supported](reference/limits.md) is the short list of things ffmpeg-wasi
deliberately will not do: no `ffmpeg` command lines, no network inputs, no hardware acceleration,
no HEVC or AV1 encode from the `.wasm` module, and a native driver for linux/amd64 only.

## Status

**Released.** Releases ship current FFmpeg (n9.0.1) as **lgpl** and **gpl** builds, in a
**lean** and an **intermediate** profile, both as portable **WASI modules** and as **native
drivers** (spec 0028, threads + SIMD, driven by afmpeg's native backend). The native driver adds
a third **full** profile with HEVC (x265) and AV1 (SVT-AV1) encode. The engine **transcodes**
(decode → filter → encode → mux) over a virtual filesystem: the `probe`, `process` (full
`filter_complex`), `frames`, and `version` ops all work today. Design: afmpeg
[spec 0007](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0007-libav-direct-engine.md).

## Further reading

Everything written about the estate, including the curated guides, is on
[the blog](https://phpboyscout.uk/topics/).

!!! tip "Ask phpbotscout"

    ![phpbotscout](https://phpboyscout.uk/images/projects/logo-phpbotscout.png){ width="84" align=left style="border-radius:10px;margin-right:1rem" }

    He answers questions about the projects over on the Discord, citing the docs
    where they already cover it, and offering to raise an issue where they don't.
    Bring a bug, an idea, or a questionable engineering decision.

    [Join the Discord](https://discord.gg/mQzGbmGyzZ){ .md-button .md-button--primary }
