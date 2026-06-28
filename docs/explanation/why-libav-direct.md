---
title: Why libav-direct
description: The FFmpeg-7.0 threading wall, the EOL trap, and why linking the libraries directly is the way under both.
date: 2026-06-28
tags: [explanation, architecture]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Why libav-direct

ffmpeg-wasi does something deliberately different from every other "FFmpeg in
WebAssembly" project: it does **not** compile the `ffmpeg` command-line tool. It compiles
FFmpeg's **libraries** (`libavformat`, `libavcodec`, `libavfilter`, …) and drives them with
a small engine of our own. This page explains why — because the reason is the whole point.

## Two walls everyone else hits

### Wall 1 — the threading rewrite (FFmpeg 7.0+)

In FFmpeg 7.0 the `ffmpeg` **command-line tool** was rewritten around a multithreaded
scheduler (`fftools/ffmpeg_sched.c`). The CLI now *requires* threads to build; you cannot
configure it `--disable-pthreads`.

WebAssembly's threading story is the problem. A pure-Go WASI runtime like
[wazero](https://wazero.io/) implements the threads *instructions* (shared memory, atomics)
but **not `wasi-threads` thread-spawn** — there is no host primitive to create a new thread.
So the modern `ffmpeg` CLI simply cannot run on a pure-Go runtime.

The usual escape — switch to a thread-capable runtime such as Wasmtime — means **CGO**, a C
runtime linked into your Go binary. That forfeits the entire reason to use WASI here:
the single static cross-compiled binary, the sandbox, the no-native-dependencies deploy.

### Wall 2 — the end-of-life trap

The one existing WASI-capable FFmpeg build pins **FFmpeg 5.1** — the last release series
*before* the threading rewrite. FFmpeg 5.1 is **end-of-life**. For a library whose entire
job is parsing **untrusted media**, shipping an unmaintained decoder with no security
backports is not a tradeoff; it's a liability.

So the field is stuck between *"current FFmpeg but needs CGO"* and *"pure-Go but EOL"*.

## The way under: link the libraries, not the CLI

Here's the insight: **the threading is only in `fftools` (the CLI).** FFmpeg's actual media
libraries — the `libav*` family — build **single-threaded** with `--disable-pthreads` and no
trouble at all. The thread scheduler is a property of the *program*, not the *codecs*.

So we skip the program. ffmpeg-wasi:

1. compiles the `libav*` libraries to `wasm32-wasi`, single-threaded; and
2. links them into a small **engine** (`src/driver.c`) that drives them directly — open
   inputs, build a filter graph, run the decode→filter→encode→mux loop, write outputs.

The engine is the part the CLI's scheduler used to be — except ours is single-threaded by
design, so it runs anywhere a WASI runtime does. No CGO. No `wasi-threads`. **Current
FFmpeg.**

## What this buys

- **Current, maintained FFmpeg** — we track upstream, so security fixes flow.
- **Pure-Go-runnable** — designed for wazero; your Go program embeds it and cross-compiles
  to one static binary.
- **Sandboxed** — the engine runs inside the WebAssembly sandbox. A memory bug in a codec
  corrupts the guest's linear memory; it cannot reach the host.
- **Independent of the dead ends** — not the EOL pin, not `wasi-threads`, not the WASI 0.2
  component model (which is an interface revolution, not a threading one — FFmpeg isn't
  written for its async/stack-switching model either).

## The cost we accept

Driving `libav*` directly means we define the **operation surface** ourselves — a structured
[job-spec vocabulary](../reference/job-spec.md), not "arbitrary `ffmpeg` CLI argument
strings." That's more work for us and a different contract for callers, but it's typed,
validated, and exactly as capable as the engine actually is — no leaky partial-CLI illusion.

It also means we **own a current FFmpeg build** — genuine porting work (wasm has no threads,
no `dup`, no `mkstemp`; see [The build](the-build.md)). That ownership is the moat: it is
why ffmpeg-wasi can be the reference current FFmpeg for WASI when everyone else is stuck at
one of the two walls.
