---
title: Tutorials
description: Learning-oriented walkthroughs.
date: 2026-06-28
tags: [tutorials]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Tutorials

Learning-oriented, start-to-finish walkthroughs.

- **[Build ffmpeg-wasi and run it](build-and-run.md)** — build current FFmpeg into a single
  `.wasm` and watch it run under a pure-Go runtime. The "it really works" tour.

!!! tip "Transcoding works today"
    The engine does real in-memory transcodes now (`probe` + the full `filter_complex`
    `process` op). Drive it from Go via [afmpeg](https://gitlab.com/phpboyscout/afmpeg)'s
    `RunJob` / `Command.JobSpec()`; the job shapes are in
    [the job-spec reference](../reference/job-spec.md).
