---
title: How-to guides
description: Task-oriented recipes.
date: 2026-06-28
tags: [how-to]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# How-to guides

Goal-oriented recipes for specific problems.

- **[Build from source](build-from-source.md)** — the clean-room Docker build (LGPL or GPL, the
  `wasm` module or the `native` driver).
- **[Choose & verify a variant](choose-a-variant.md)** — pick the right artifact and check its
  checksum.

From Go (via [afmpeg](https://gitlab.com/phpboyscout/afmpeg)):

- **Consume a released module** — `WithModuleURL(<release asset>, WithSHA256(…))`.
- **Run a transcode** entirely in memory — build a `Command` and `RunJob`; see
  [the job-spec vocabulary](../reference/job-spec.md) for the `process`/`probe` shapes.

## When a job fails

- **Read the stderr line first** — it names the cause. [Errors & exit codes](../reference/errors.md)
  maps every message to what to change.
- **Check the capability is in your profile** — most "unknown encoder" and "bad filtergraph"
  failures are a `lean` module being asked for something in `intermediate`. See
  [codecs](../reference/codecs.md), [filters](../reference/filters.md) and
  [containers](../reference/containers.md).
- **Check it is supported at all** — [limits](../reference/limits.md) lists what no build does.
