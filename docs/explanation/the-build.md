---
title: The build
description: How libav* is cross-compiled to wasm32-wasi — the toolchain, the single-threaded config, setjmp/longjmp, and the wasi compat shims.
date: 2026-06-28
tags: [explanation, build]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# The build

How `libav*` becomes a `.wasm`. The pipeline is three small scripts under `build/`,
orchestrated by a Dockerfile; this page explains the parts that aren't obvious.

## The toolchain (`build/toolchain.sh`)

Everything is built with the [wasi-sdk](https://github.com/WebAssembly/wasi-sdk) — an
LLVM/clang toolchain targeting `wasm32-wasip1` with a `wasi-libc` sysroot. The notable flags:

- **`--target=wasm32-wasip1 --sysroot=…`** — cross-compile to WASI.
- **The WebAssembly feature set** — `-mtail-call -mbulk-memory -msimd128 -mextended-const
  -mnontrapping-fptoint -msign-ext -mmutable-globals -mreference-types`. These are kept in
  lock-step with what the [afmpeg](https://gitlab.com/phpboyscout/afmpeg) runtime enables; a
  module built with a feature the runtime doesn't allow won't instantiate.
- **`-Oz`** — optimise for size (the module is shipped over the wire).

## Single-threaded by configuration

The reason this works at all (see [Why libav-direct](why-libav-direct.md)): the libraries
are configured `--disable-pthreads --disable-w32threads --disable-os2threads`. The threading
that blocks the FFmpeg CLI lives in `fftools`, which we don't build (`--disable-programs`).
We also `--disable-asm` (no wasm assembly path) and `--disable-network`.

## setjmp / longjmp

FFmpeg's C uses `setjmp`/`longjmp` (notably in some codecs' error handling). clang lowers
these for wasm with **`-mllvm -wasm-enable-sjlj`**, which emits two host imports —
`env.__wasm_setjmp` and `env.__wasm_longjmp`. The runtime provides them; afmpeg implements
them with wazero's snapshotter, and the bundled `tools/run` harness does the same. This is
why a stock WASI runtime can't load the module but our setup can.

## The wasi compatibility shims

WASI is a smaller world than POSIX, and FFmpeg assumes POSIX. wasi-libc deliberately omits a
few functions ("WASI has no …"). We bridge the gap minimally:

- **`config.h` fixups** — `HAVE_SYSCTL`, `HAVE_MKSTEMP`, `HAVE_GETHRTIME`, `HAVE_SETRLIMIT`
  are forced off so FFmpeg takes its portable fallbacks.
- **`build/wasi-compat.h`** — force-included during the libav\* build (via `--extra-cflags`,
  so it's baked into `config.mak`) to **declare** functions wasi-libc's headers gate out
  (e.g. `dup`, `tempnam`), keeping the strict-C99 clang from erroring.
- **`build/wasi-compat.c`** — **implements** the symbols the link actually needs. For
  example WASI has no `dup(2)`, so we map it onto `fcntl(F_DUPFD)` (which wasi-libc backs
  with `fd_renumber`).

This shim layer is small and explicit — and it's exactly the kind of porting work that
"owning a current FFmpeg build" means. It grows as new codecs/protocols pull in new corners
of POSIX.

## The artifact

`build/driver.sh` links `src/driver.c` + the compat shims against the `libav*` archives into
a single WASI **command** module. clang adds the `_start`/crt1 entry automatically; no
wasm-ld-only flags are needed. The result is `dist/ffmpeg-wasi-<variant>.wasm`.

## Reproducibility

Inputs are pinned in `build/versions.lock` (the FFmpeg tag, the wasi-sdk image). A release
tag is `<FFMPEG_VERSION>-<build-rev>` (e.g. `n8.1.2-1`); the build revision bumps when the
toolchain or config changes for the same upstream FFmpeg.
