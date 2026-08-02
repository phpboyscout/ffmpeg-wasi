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
| `1` | **a processing failure** during `process`/`frames` — an input would not open, an encoder would not start, a filtergraph would not configure. The libav error is rendered on stderr |
| `2` | **malformed request** — invalid job-spec JSON, an unknown `op`, or a spec whose shape is wrong (e.g. `probe` without an `inputs` array) |
| `3` | **vocabulary too new** — the spec's `version` exceeds what this engine supports (`version-too-new`); the distinct code lets a caller tell "upgrade the engine" from "fix the spec" |

`process` and `frames` collapse every runtime failure to `1`; the specific cause is the stderr line.
[Errors & exit codes](errors.md) maps each message to its cause and its code — **including two
`process` validation failures that currently exit `0`**, which a host keying only on the exit code
must know about.

## Version negotiation

The engine advertises the highest [job-spec vocabulary version](job-spec.md#versioning) it
understands. A host preflights it **before** running jobs:

```jsonc
{ "op": "version" }        // → {"vocab_version":9,"ffmpeg_version":"n8.1.2"}
```

A `version` query carries no `version` field of its own, which is what makes it answerable by an
engine older than the caller. **The exemption is a convention on the caller's side, not a special
case in the engine**: the gate runs before dispatch and inspects every spec, so stamping a `version`
onto a `version` query would get that query rejected with exit `3` — defeating the one call designed
to detect exactly that mismatch. Send it unstamped.

Every `process`/`probe` spec should be stamped with the `version` it was written in; if that exceeds
the engine's, the engine exits `3` rather than silently dropping the fields it doesn't understand. A
module that doesn't answer `op:"version"` (a pre-gate engine, or a generic non-ffmpeg-wasi module)
carries no vocabulary contract and is tolerated.

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
  is a raw `open()` against the host filesystem rather than the caller's, so the progress emitter is
  effectively inert (native progress is byte-observed host-side instead — afmpeg spec 0032/0033).

### A concat input needs a writable `/tmp` on the WASM target

`inputs[].concat` opens libavformat's concat demuxer, which needs an `ffconcat` playlist file. On
the WASM module the engine writes that playlist to **`/tmp/afmpeg-concat-<n>.txt`** on the mounted
filesystem before opening it, so **a writable `/tmp` is a requirement of the mount for concat
jobs** — and only for concat jobs; nothing else in the engine writes a scratch file. Without it the
job fails with `process: cannot create concat list`.

The native driver has no such requirement: with `AFMPEG_NATIVE_SOCKET` set it builds the playlist in
memory and routes each segment open over the IPC bridge, so no scratch file exists.

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

| Frame | Bytes sent | Reply |
|---|---|---|
| Open | `'O'`, mode (`'r'`\|`'w'`), `nameLen` (u32), `name` | status (1 byte: `0` ok, non-zero error) |
| Read | `'R'`, count (u32) | count (u32) + that many bytes |
| Write | `'W'`, len (u32), bytes | **count written (u32)** |
| Seek | `'S'`, offset (i64), whence (u8) | new position (i64) |
| Size | `'Z'` | size (i64) |
| Close | `'C'` | — (ends the session) |

All integers are **little-endian**. Two details a host implementation has to get right:

- **A `Read` reply of `0` means end-of-file**, not a short read — the driver turns it into
  `AVERROR_EOF`. Return the bytes you have, or `0` when there are none left.
- **`Write` replies with a count, not a status byte.** The driver passes that count straight back to
  libav as the number of bytes written.

`Seek`'s `whence` arrives with libav's `AVSEEK_FORCE` bit already masked off, so it is a plain
`SEEK_SET`/`SEEK_CUR`/`SEEK_END`. `AVSEEK_SIZE` is never sent as a `Seek` — it becomes the `Size`
frame.

The driver's AVIO buffer is **64 KiB**, so reads and writes arrive in chunks of about that size.
Write mode must open `O_RDWR` (not append), so a muxer's backward seeks — e.g. the non-fragmented
MP4 `moov`/`mdat` patch on `av_write_trailer` — can overwrite earlier bytes. The concat demuxer's
per-segment opens route over the same bridge. The reference host implementation is afmpeg's
[`pkg/afmpeg/native`](https://gitlab.com/phpboyscout/afmpeg/-/tree/main/pkg/afmpeg/native).

## Where this is implemented

`src/driver.c` (dispatch, gate, `--report`), `src/nativeio.c` (the IPC bridge), and afmpeg's
`internal/vfs` (WASI devices) + `pkg/afmpeg/native` (IPC host). For how these fit together, see
[Inside the engine](../explanation/engine-internals.md).

## Related

- [Errors & exit codes](errors.md) — every stderr message, its cause and its code.
- [Limits & what is not supported](limits.md) — the caps and absences a host should expect.
- [The job-spec vocabulary](job-spec.md) — the JSON this contract carries.
