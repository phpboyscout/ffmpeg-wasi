---
title: Limits & what is not supported
description: The hard caps a job runs into, the capabilities deliberately absent from every build, and what to reach for instead.
date: 2026-08-02
tags: [reference, limits]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Limits & what is not supported

Everything on this page is a **deliberate** boundary — either a compile-time cap in the engine or a
capability left out of the build. None of it is a bug, and none of it is planned work unless it says
so. Where something is genuinely absent, the alternative is named.

For what *is* available see [codecs](codecs.md), [filters](filters.md) and
[containers & protocols](containers.md); for the messages you get when you cross one of these lines,
see [errors & exit codes](errors.md).

## Can I pass an `ffmpeg` command line?

No. The engine accepts a [structured job spec](job-spec.md) and nothing else — there is no argument
parser for `-i`, `-vf`, `-c:v` or any other CLI flag, because the `ffmpeg` command-line tool is not
built (`--disable-programs`). This is the central design decision, not an omission:
[Why libav-direct](../explanation/why-libav-direct.md) explains it.

The one place ffmpeg's own syntax survives is the `filter` field, which is a real
`filter_complex` string parsed by `avfilter_graph_parse2`. Everything around it — inputs, outputs,
codecs, encoder and muxer options — is typed JSON.

## Can an input or output be a URL?

No. `libav*` is configured `--disable-network` in both the WASM and the native build, and the only
protocols enabled are **`file`** and **`pipe`**. An `http://`, `https://`, `rtmp://` or `srt://`
path is not openable, and the engine has no code to fetch one.

Every path in a job spec resolves against the filesystem the host mounts — for the WASM module over
WASI syscalls, for the native driver over the
[IPC bridge](driver-invocation-abi.md#native-ipc-contract-backend-b). Download the media first and
present it on that filesystem.

The same applies to a **segmenting output**: an `hls` or `dash` job writes its playlist and segments
as files onto the mounted filesystem. Nothing is uploaded or served.

## Does a job use more than one CPU core?

Only on the native driver.

- **The WASM module is single-threaded**, unconditionally. `libav*` is configured
  `--disable-pthreads --disable-w32threads --disable-os2threads`, so every decoder, filter and
  encoder runs on one thread. It also has no assembly path (`--disable-asm --disable-x86asm
  --disable-runtime-cpudetect`), so there is no SIMD-accelerated inner loop either. That combination
  is what lets it run on [wazero](https://wazero.io/), which has no thread-spawn primitive.
- **The native driver has real threads and SIMD**, which is the entire reason it exists — the same
  jobs run 48–58× faster on software encode. See
  [choose a variant](../how-to/choose-a-variant.md#which-runtime).

Setting a `threads` option in `outputs[].options` does not change this on WASM; the encoder has no
threading to enable.

## Can I encode HEVC or AV1 from the `.wasm` module?

No. Both heavy encoders are **native-only** and live in the `full` profile:

| | Encoder | Runtime | Variant |
|---|---|---|---|
| **HEVC / H.265** | `libx265` | native driver only | **gpl only** |
| **AV1** | `libsvtav1` | native driver only | both |

There is no `full` WASM module and there will not be one: `build/libav.sh` refuses the combination
outright, exiting `2` with `PROFILE=full is native-only` if you ask for it. Both encoders are
thread- and SIMD-hungry, which is exactly what the WASM target does not have.

**Decode is unaffected.** HEVC decodes in every profile including `lean`, and AV1 decodes (via
libdav1d) in `intermediate` and `full` on **both** runtimes.

## Can I use a GPU or a hardware encoder?

No. No hardware acceleration is enabled in any build — no NVENC, VAAPI, QSV, VideoToolbox or
hwaccel decoder. Every codec is a software one. Hardware encoders are the remaining unimplemented
members of the `full` profile and are deferred until a GPU-equipped runner exists.

## Which platforms does the native driver run on?

**linux/amd64 only.** The published assets are `ffmpeg-wasi-driver-linux-amd64-*`; there is no
macOS, Windows or arm64 driver. The build is not cross-architecture — `build/Dockerfile.native`
compiles with the host toolchain on a `debian:bookworm-slim` base.

If you need another platform, the **WASM module is architecture-independent** and runs anywhere a
WASI runtime does. That is the portable tier; native is the speed tier.

## How many inputs, outputs and streams can one job have?

The engine's context is fixed-size, so a job has hard caps. All of them are compile-time constants
in `src/process.c` and `src/frames.c`:

| Limit | Cap | Crossing it |
|---|---:|---|
| `inputs[]` entries | **32** | fails with `Invalid argument` (no specific message) |
| `outputs[]` entries | **8** | `process: too many outputs` |
| Filter-graph **input** pads | **32** | `process: too many graph inputs (max 32)` |
| Filter-graph **output** pads | **16** | fails with `Invalid argument` (no specific message) |
| Stream-**copied** streams across all outputs | **32** | `process: too many copied streams` |
| Subtitle streams across all outputs | **16** | `process: too many subtitle streams` |

These are per *job*, not per output. A four-output job that copies eight streams into each uses 32
of the copy budget.

## How many frames can one `frames` job emit?

- **`count` absent → 1000.** An uncapped `interval` over a long input would otherwise run away, so
  the engine applies a built-in default cap of 1000 frames.
- **4096 targets, absolute.** The selector's target list is bounded at 4096 timestamps regardless of
  `count`, so an `interval` fine enough to imply more simply stops there.
- **`count` above 4096 does not raise the ceiling** for the seeking selectors — it caps output, it
  does not extend the target list. The `scene` selector streams rather than seeking, so `count` is
  its only bound.

## Why does a `concat` input fail with "cannot create concat list"?

Because the WASM module writes the concat playlist to **`/tmp`** on the mounted filesystem, and the
host has not made `/tmp` writable.

`inputs[].concat` joins like-codec files through libavformat's concat demuxer, which needs an
`ffconcat` playlist to open. On the WASM target the engine materialises that playlist at
`/tmp/afmpeg-concat-<n>.txt` before opening it. **A writable `/tmp` is therefore a requirement of
the mounted filesystem for concat jobs**, and only for concat jobs — nothing else in the engine
writes a scratch file.

The native driver does not have this requirement: with `AFMPEG_NATIVE_SOCKET` set it builds the
playlist in memory and routes every segment open over the IPC bridge, so no scratch file exists.

## Which fonts can `drawtext` and `subtitles` use?

Only the ones you mount. There is **no fontconfig** in the build and the sandbox has no system font
directory, so a font must be named by path:

```text
drawtext=fontfile=/font.ttf:text='hello'
subtitles=filename=sub.srt:fontsdir=/fonts:force_style='FontName=DejaVu Sans'
```

A `drawtext` filter with no `fontfile` fails; it does not fall back to a default face. Both filters
need the **intermediate** profile — see [filters](filters.md).

## What is deliberately left out of the codec and filter sets

The build starts from `--disable-everything` and enables an explicit allowlist, so anything not
listed in [codecs](codecs.md) or [filters](filters.md) is absent by construction. The omissions
people ask about specifically:

- **`eq`** — GPL-only in FFmpeg and not enabled in *either* variant. Use `curves`,
  `colorbalance` or `colorchannelmixer` for level and gamma adjustment.
- **DTS encode** — `dca` decodes; there is no DTS encoder, because FFmpeg's is experimental.
- **TrueHD and AMR** — neither decode nor encode, in any profile.
- **Decode-only formats.** ProRes, DNxHD, DV, MPEG-2, MPEG-4 Part 2, VC-1, WMV3 and Theora decode in
  the `intermediate` profile but have **no encoder** enabled. A job that names one as
  `video_codec` fails with `process: unknown encoder <name>`.
- **The Ogg *muxer* is `intermediate`-only.** Ogg *demuxes* in `lean`, so an Ogg input works
  everywhere while writing an `.ogg` output needs the larger profile.

## What the sandbox does not protect against

The WASM module confines a codec bug to the guest's linear memory — a memory-safety failure while
parsing untrusted media cannot reach the host. Two things it does not do:

- **The native driver is not sandboxed.** It is an ordinary host process; the IPC bridge keeps its
  *file I/O* inside the caller's filesystem, but a codec bug runs with the host process's
  privileges. That is the security-posture side of the speed trade, and the reason WASM stays the
  default.
- **Neither target bounds resource use.** There is no time limit, memory ceiling or output-size cap
  in the engine. A pathological input can consume as much CPU and memory as the host allows; that
  budget is the host's to enforce.

## Related

- [Errors & exit codes](errors.md) — the message for each failure above.
- [Build options](build-options.md) — the knobs that decide which of these limits apply.
- [Variants & artifacts](variants.md) — what each profile ships.
