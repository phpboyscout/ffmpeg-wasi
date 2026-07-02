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
  example WASI has no `dup(2)` and no way to allocate a fresh lowest-available fd, so it is
  a **stub that fails with `ENOSYS`** — enough to satisfy the link (libavformat's `file.o`
  references `dup`) while the code paths that would call it are never exercised, because the
  real file I/O goes over the mounted filesystem.

This shim layer is small and explicit — and it's exactly the kind of porting work that
"owning a current FFmpeg build" means. It grows as new codecs/protocols pull in new corners
of POSIX.

Not every POSIX gap is a *link-time* one. Some are **runtime devices**. libavutil's
`av_get_random_seed()`, for instance, reads `/dev/urandom` (its only compiled entropy source
here) and otherwise falls back to a `clock()`-jitter loop that never terminates under WASI —
so any format needing a random id (the Matroska muxer seeds track UIDs this way) would hang.
That gap is filled at the *runtime* layer, not here: the afmpeg host serves `/dev/urandom`
from its vfs bridge. See afmpeg's
[vfs-bridge explanation](https://afmpeg.phpboyscout.uk/explanation/components/vfs-bridge/#why-devurandom-is-load-bearing).

## The openh264 dependency

Both variants encode H.264 via [openh264](https://github.com/cisco/openh264) (the GPL variant
additionally offers libx264). openh264 is C++ with a GNU-make build that doesn't know about wasm,
so `build/deps.sh` cross-compiles it with a few deliberate overrides — `OS=linux` (steers the
Makefile only; the C preprocessor never sees `__linux__` for wasm), `ARCH=generic USE_ASM=No`
(portable C path), `USE_STACK_PROTECTOR=No`, and `-fno-exceptions -fno-rtti`. Three small wasm
adaptations make it build and run, all in `build/openh264-wasi.patch`:

- **No `<sys/sysctl.h>` / `SCHED_FIFO`** — wasip1 lacks both; the patch teaches `WelsThreadLib`
  the `__wasi__` case (CPU count is simply 1).
- **Single-threaded pthread/sem shim** (`build/openh264-threads.c`) — wasip1 has no thread
  spawning. The encoder runs single-threaded (libav\* is `--disable-pthreads`, so FFmpeg requests
  one thread), so the mutex/sem operations are no-op successes and `pthread_create` is never
  reached. The shim is archived **into** `libopenh264.a` so it satisfies both FFmpeg's configure
  probe and the final link.
- **A 2-argument `ForceIntraFrame`** — openh264's C vtable (which FFmpeg calls through) declares
  `ForceIntraFrame(self, bool)`, but its C++ method is `(bool, int iLayerId = -1)`. On native ABIs
  the arity slip is harmless; wasm's strict indirect-call typing traps on it, so the patch drops
  the parameter (hardcoding the upstream `-1` default).

openh264's C++ runtime is pulled in at the engine link with `-lc++ -lc++abi`. The codec's
*patent* posture (self-compiled → outside Cisco's grant) is a [licensing](licensing.md#h264-encode-and-the-avc-patent-pool)
matter, not a build one.

## The artifact

`build/driver.sh` links `src/driver.c` + the compat shims against the `libav*` archives into
a single WASI **command** module. clang adds the `_start`/crt1 entry automatically; no
wasm-ld-only flags are needed. The result is `dist/ffmpeg-wasi-<variant>.wasm`.

## Reproducibility

Inputs are pinned in `build/versions.lock` (the FFmpeg tag, the wasi-sdk image). A release
tag is `<FFMPEG_VERSION>-<build-rev>` (e.g. `n8.1.2-1`); the build revision bumps when the
toolchain or config changes for the same upstream FFmpeg.
