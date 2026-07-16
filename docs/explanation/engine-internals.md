---
title: Inside the engine
description: A maintainer's map of src/ — what each translation unit does, how a job flows through them, and what the wasm and native builds share versus split.
date: 2026-07-16
tags: [explanation, engine, native]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Inside the engine

The engine is deliberately small — roughly **2,900 lines of C** under `src/`, plus vendored cJSON.
It has no public C API to call: you drive it as a binary over the [invocation
contract](../reference/driver-invocation-abi.md), passing a [JSON job spec](../reference/job-spec.md).
This page is the **maintainer's map** — what each translation unit owns and how a job flows through
them — for anyone reading or changing the C. (Why it's built this way at all is [Why
libav-direct](why-libav-direct.md); how it's compiled is [The build](the-build.md).)

## The translation units

| Unit | Lines | Owns |
|---|---|---|
| `driver.c` | ~270 | **Entry point & dispatch.** `main()` reads the spec from `argv[1]`, runs the [vocabulary gate](../reference/job-spec.md#versioning), and dispatches on `"op"` to one of the four handlers. Also holds `op:probe`, `op:version`, and the `--report` build smoke test. Defines `AFMPEG_VOCAB_VERSION`. |
| `process.c` | ~1,500 | **The transcode/filter/mux engine** (`op:process`) — the heart. Opens inputs, builds the `filter_complex` graph (`avfilter_graph_parse2`), routes each graph pad by `map` to its output, encodes or stream-copies, muxes, and runs the decode→filter→encode→mux loop. Also the analysis-filter collection and the subtitle transcode lane. |
| `frames.c` | ~500 | **Still-frame extraction** (`op:frames`) — the four selectors (`timestamp`/`timestamps`/`interval`/`scene`), seek-and-decode-forward, optional scale, and templated image output. |
| `meta.c` | ~90 | **Metadata & chapters** — shared helpers for reading/writing container and per-stream tags, disposition flags, and chapters. Used by both `probe` (read) and `process` (write). |
| `nativeio.c` | ~370 | **The native AVIO-over-IPC bridge** — native build only (`-DAFMPEG_NATIVE`). Installs a seekable `AVIOContext` that speaks the [framed IPC protocol](../reference/driver-invocation-abi.md#native-ipc-contract-backend-b) to the host, so the native driver reads and writes the caller's filesystem over a Unix socket instead of the host disk. |
| `progress.c` | ~80 | **The progress emitter** (spec 0032) — a one-way, best-effort NDJSON writer to `/dev/afmpeg-progress`, throttled to ~100 ms of media time. Inert unless the job set `"progress":true` and the device opens. |
| `third_party/cJSON` | — | Vendored JSON parse/print — the spec in, the results out. |

Each `*.c` has a matching `*.h` declaring its small surface to `driver.c`.

## How a job flows

1. **`main()` (`driver.c`)** parses `argv[1]`, applies the version gate, and dispatches on `"op"`.
2. For **`process`**, control enters **`process.c`**: inputs are opened (through `nativeio.c`'s AVIO
   on the native build, or WASI syscalls on the wasm build), the filtergraph is parsed and wired,
   and the decode→filter→encode→mux loop runs. `meta.c` applies any metadata/chapters; `progress.c`
   emits records as packets mux (when enabled).
3. Results are serialised with **cJSON** to stdout, one line; errors go to stderr with a non-zero
   [exit code](../reference/driver-invocation-abi.md#exit-codes).

`probe`, `frames`, and `version` are the smaller paths off the same dispatch.

## What the two builds share — and split

Both targets compile the **same** engine source; the difference is I/O and the toolchain, selected by
the `TARGET` build variable ([The build](the-build.md)):

- **Shared** — all of `driver.c`, `process.c`, `frames.c`, `meta.c`, `progress.c`, and the whole
  job-spec vocabulary. The behaviour a consumer sees is identical.
- **`TARGET=wasm`** — cross-compiled to `wasm32-wasi`; file I/O is WASI syscalls the host bridges to
  an `afero.Fs`; `/dev/afmpeg-progress` is a synthesised WASI device.
- **`TARGET=native`** — a host ELF with real threads + SIMD; `nativeio.c` is compiled in
  (`-DAFMPEG_NATIVE`) and file I/O rides the IPC bridge. `progress.c`'s raw `open()` of the progress
  device finds no host file, so engine-side progress is inert on native (host-observed byte progress
  covers it — afmpeg spec 0032/0033).

## Related

- [Driver invocation & ABI](../reference/driver-invocation-abi.md) — the external contract these
  internals implement.
- [The job-spec vocabulary](../reference/job-spec.md) — the fields `process.c`/`driver.c` parse.
- [The build](the-build.md) — how the units become a `.wasm` or a native driver.
- [Why libav-direct](why-libav-direct.md) — why there's a custom engine at all.
