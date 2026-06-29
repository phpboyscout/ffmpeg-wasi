---
title: Explanation
description: How and why ffmpeg-wasi works the way it does.
date: 2026-06-28
tags: [explanation]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Explanation

Understanding-oriented discussion.

- **[Why libav-direct](why-libav-direct.md)** — the FFmpeg-7.0 threading wall, the EOL trap,
  and why linking the libraries directly is the way under both. The headline story.
- **[The licensing model](licensing.md)** — MIT tooling, LGPL/GPL artifacts, and why shipping
  both is clean.
- **[The build](the-build.md)** — wasi-sdk, the single-threaded `libav*` config,
  setjmp/longjmp lowering, and the POSIX/WASI compat shims.
- **[Release signing](signing.md)** — the KMS key only the tag pipeline can wield, the detached
  signature over `checksums.txt`, what it defends, and where the public key lives.
