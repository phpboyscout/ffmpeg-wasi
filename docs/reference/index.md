---
title: Reference
description: Accurate, structured facts.
date: 2026-06-28
tags: [reference]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Reference

Information-oriented, accurate facts.

## Driving the engine

- **[The job-spec vocabulary](job-spec.md)** — the `process` / `probe` / `frames` / `version`
  operations; the compatibility contract with afmpeg.
- **[Driver invocation & ABI](driver-invocation-abi.md)** — how a host drives the binary: argv,
  stdio, exit codes, the filesystem devices, and the native IPC framing.
- **[Errors & exit codes](errors.md)** — every message the engine writes to stderr, what caused it,
  and what to change.
- **[Limits & what is not supported](limits.md)** — the hard caps a job runs into and the
  capabilities deliberately absent from every build.

## What a build contains

- **[Codecs](codecs.md)** — the decoders and encoders enabled per profile.
- **[Filters](filters.md)** — the libavfilter vocabulary available inside the `filter` string.
- **[Containers, bitstream filters & protocols](containers.md)** — the demuxers and muxers per
  profile, and the two protocols the engine can open.

## Building and shipping

- **[Build options](build-options.md)** — every Docker build argument, environment variable and
  pinned dependency version, and what happens when one is wrong.
- **[Variants & artifacts](variants.md)** — the LGPL/GPL builds as both **WASI modules** and
  **native drivers**, the lean/intermediate/full profiles, the release assets, and versioning.
