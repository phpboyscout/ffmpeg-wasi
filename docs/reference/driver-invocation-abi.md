---
title: Driver invocation & ABI
description: The contract for driving the engine binary — argv, the four ops, stdio, exit codes, the filesystem devices it expects, and the native driver's AFMPEG_NATIVE_SOCKET IPC framing.
date: 2026-07-16
tags: [reference, api, native]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Driver invocation & ABI

The engine ships as a single binary — a `wasm32-wasi` module or a native ELF `driver` (both built
from `src/driver.c`). This page is the **host contract**: how a program invokes it, what it writes,
how it exits, and — for the native driver — the IPC protocol it speaks for file I/O.
[afmpeg](https://gitlab.com/phpboyscout/afmpeg) is the reference host; this reference is what you'd
implement to drive the engine from anything else. The *job* vocabulary (the JSON you pass) is the
separate [job-spec reference](job-spec.md); this page is the *invocation* around it.

## Invocation

The engine reads **one argument — the JSON job spec** — and dispatches on its `"op"`:

```
driver '<json-spec>'
```

With **no arguments, or `--report`**, it prints a capability report instead (engine name, vocabulary
version, the linked FFmpeg/libav\* versions, and a probe of a few encoders/decoders) — a build smoke
test, not a job. Paths inside the spec resolve against the mounted filesystem (see
[Filesystem & devices](#filesystem-devices)).

## Operations

Four ops, dispatched on the spec's `"op"` string. Their field vocabulary is the
[job-spec reference](job-spec.md); this is just the dispatch surface.

| `op` | Input | Output |
|---|---|---|
| `probe` | `inputs[]` | container/stream info as JSON on stdout |
| `process` | full spec | transcode/filter/mux; a per-output result JSON on stdout, files written to the fs |
| `frames` | one video input + a selector | still frames written to the fs; a per-frame result JSON |
| `version` | none | `{"vocab_version":N,"ffmpeg_version":"…"}` on stdout |

## Standard streams

- **stdout** — the result: one line of unformatted JSON (probe info, process/frames result, or the
  version reply). Nothing else is written there.
- **stderr** — a human-readable error message on failure.
- **exit code** — `0` on success, non-zero on failure (see below).

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `2` | **malformed request** — invalid job-spec JSON, an unknown `op`, or a spec whose shape is wrong (e.g. `probe` without an `inputs` array) |
| `3` | **vocabulary too new** — the spec's `version` exceeds what this engine supports (`version-too-new`); the distinct code lets a caller tell "upgrade the engine" from "fix the spec" |
| other non-zero | a processing failure during `process`/`frames` (with a stderr message) |

## Version negotiation

The engine advertises the highest [job-spec vocabulary version](job-spec.md#versioning) it
understands. A host preflights it **before** running jobs:

```jsonc
{ "op": "version" }        // → {"vocab_version":9,"ffmpeg_version":"n8.1.2"}
```

`version` carries no `version` field of its own, so it is **exempt from the gate** — even an engine
older than the caller answers it. Every `process`/`probe` spec should be stamped with the `version`
it was written in; if that exceeds the engine's, the engine exits `3` rather than silently dropping
the fields it doesn't understand. A module that doesn't answer `op:"version"` (a pre-gate engine, or
a generic non-ffmpeg-wasi module) carries no vocabulary contract and is tolerated.

## Filesystem & devices

The engine does **all** media I/O through a filesystem the host provides — never the host's real
disk (that is the whole sandbox thesis). Beyond the input/output paths named in the spec, the engine
expects a small set of character devices:

| Device | Purpose |
|---|---|
| `/dev/urandom`, `/dev/random` | entropy — libav seeds some muxers (e.g. Matroska) from it |
| `/dev/null` | the standard sink |
| `/dev/afmpeg-progress` | **write-only** progress side-channel; the engine streams NDJSON progress records here when a `process` job sets `"progress":true` ([job-spec §Progress](job-spec.md#progress-side-channel)) |

How those are served differs by target:

- **WASI module** — the host mounts a filesystem over WASI syscalls; afmpeg's `internal/vfs`
  synthesises all four devices (`/dev/urandom` from a CSPRNG, `/dev/afmpeg-progress` fed to the
  `WithProgress` channel). Media paths hit the mounted filesystem.
- **Native driver** — the ELF has no WASI. Media paths are served over the IPC bridge below. The
  devices are **host devices**: `/dev/urandom`/`/dev/null` are the real ones; `/dev/afmpeg-progress`
  is a raw `open()` that finds no such host file, so the progress emitter is inert (native progress
  is byte-observed host-side instead — afmpeg spec 0032/0033).

## Native IPC contract (Backend B)

The native driver replaces WASI file I/O with a **seekable AVIO-over-IPC** bridge (`src/nativeio.c`),
so it too touches no host disk — the host serves the caller's filesystem over a Unix socket.

- **Activation** — the driver speaks IPC only when the environment variable **`AFMPEG_NATIVE_SOCKET`**
  names a Unix socket path. Absent it, the native build behaves like a plain program.
- **One connection per opened file** — the driver's custom `AVIOContext` dials the socket each time
  libav opens a media path.
- **Protocol version** — the connection opens with a single version byte (**`1`**), which the host
  validates.

Each connection is one file session:

| Frame | Bytes | Reply |
|---|---|---|
| Open | `'O'`, mode (`'r'`\|`'w'`), `nameLen` (u32), `name` | status (1 byte: `0` ok, non-zero error) |
| Read | `'R'`, count (u32) | count (u32) + bytes |
| Write | `'W'`, len (u32), bytes | status |
| Seek | `'S'`, offset (i64), whence | new position (i64) |
| Size | `'Z'` | size (i64) |
| Close | `'C'` | — (ends the session) |

All integers are **little-endian**. Write mode opens `O_RDWR` (not append), so a muxer's backward
seeks — e.g. the non-fragmented MP4 `moov`/`mdat` patch on `av_write_trailer` — can overwrite earlier
bytes. The concat demuxer's per-segment opens route over the same bridge. The reference host
implementation is afmpeg's [`pkg/afmpeg/native`](https://gitlab.com/phpboyscout/afmpeg/-/tree/main/pkg/afmpeg/native).

## Where this is implemented

`src/driver.c` (dispatch, gate, `--report`), `src/nativeio.c` (the IPC bridge), and afmpeg's
`internal/vfs` (WASI devices) + `pkg/afmpeg/native` (IPC host). For how these fit together, see
[Inside the engine](../explanation/engine-internals.md).
