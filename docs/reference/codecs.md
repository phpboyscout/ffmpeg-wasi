---
title: Codecs
description: The libavcodec decoders and encoders enabled per build profile — the codecs the engine can read and write.
date: 2026-07-03
tags: [reference, codecs]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Codecs

What the engine can **decode** (as inputs) and **encode** (as `outputs[].video_codec` /
`audio_codec`), by [profile](variants.md). Every codec here is in-tree libavcodec — no external
library, LGPL-clean (the GPL variant additionally links **libx264** for H.264 encode).

## Lean (all builds)

| | Decode | Encode |
|---|---|---|
| **Video** | h264, hevc, vp8, vp9, mjpeg, png, rawvideo | mjpeg, png, **openh264** (H.264; +**libx264** in gpl) |
| **Audio** | aac, mp3, opus, vorbis, flac, pcm_s16le, pcm_f32le | aac, flac, pcm_s16le |

## Intermediate (+ over lean)

The native codec batch (spec [0016](https://afmpeg.phpboyscout.uk/development/specs/0016-native-codec-batch/))
— all in-tree, LGPL-clean:

| | Decode | Encode |
|---|---|---|
| **Broadcast/film audio** | ac3, eac3, dca (DTS) | ac3 |
| **Lossless / legacy audio** | alac, wmav2 | alac |
| **PCM family** | pcm_s24le, pcm_s32le, pcm_f64le, pcm_u8, pcm_s16be, pcm_s24be, pcm_mulaw, pcm_alaw | pcm_s24le, pcm_s32le, pcm_f32le, pcm_mulaw, pcm_alaw |
| **Images** | gif, bmp, tiff | gif, bmp, tiff |
| **Editing intermediates** | prores, dnxhd, dvvideo | — |
| **Legacy / broadcast video** | mpeg2video, mpeg4, vc1, wmv3, theora | — |

### External encoders (spec 0018)

The LGPL/BSD encoder libraries cross-compiled into the build (`build/deps.sh`) and linked by
libav — external code, but all LGPL-compatible, so both licence variants get them. Encode
uses the `lib*` name (e.g. `audio_codec: "libopus"`); the produced stream probes back to the
plain codec name (`opus`).

| | Encode (name) | Produces | Notes |
|---|---|---|---|
| **Opus** | `libopus` | opus | 48 kHz only; resample first |
| **MP3** | `libmp3lame` | mp3 | |
| **Vorbis** | `libvorbis` | vorbis | on libogg |
| **VP8** | `libvpx` | vp8 | WebM video |
| **VP9** | `libvpx-vp9` | vp9 | WebM video; **slow** single-threaded |
| **WebP** | `libwebp` | webp | still/animated images |

All are **software, single-threaded** — VP9 encode in particular is slow without threads, so
pick VP8 unless VP9 is required. libvpx's encoder uses setjmp/longjmp, lowered to wasm
exception-handling; the afmpeg runtime enables that feature to load the module.

### Subtitles (spec 0019)

The intermediate profile also enables the **subtitle codec batch**, which rides the dedicated
subtitle transcode lane (decode → re-encode), not the filter graph. Drive it with `subtitle_codec`
on a subtitle output stream — see the [job spec](job-spec.md); for *burning* subtitles into video
use the `subtitles`/`ass`/`drawtext` [filters](filters.md) instead.

| | Codecs |
|---|---|
| **Decode** | subrip, ass, webvtt, movtext |
| **Encode** | srt, ass, webvtt, mov_text |

(`movtext` is the configure-time name; the encoder is `mov_text` at runtime — the MP4/MOV
timed-text codec.)

### Notes

- **DTS is decode-only** (`dca`) — the encoder is experimental in FFmpeg and left out
  (D-0016-B). TrueHD/AMR are not in this batch.
- **`eq`** is not a codec — see [filters](filters.md).
- **HEVC/AV1 encode** live in the **full** profile below (native only). **AV1 *decode*** is via
  **libdav1d** (spec 0023 D-0023-C) — **native only** (dav1d is thread-architected, and FFmpeg's
  in-tree `av1` decoder is hwaccel-only, so software AV1 decode needs the lib), in the intermediate
  and full native drivers, both variants. WASM cannot decode AV1 (a separate threads spike).
- **Patent posture** — AC-3, DTS, MPEG-2/4, VC-1 carry codec patents, the same class as the
  H.264/HEVC the engine already decodes; see the [licensing explanation](../explanation/licensing.md).
- The **decode-only** codecs (prores, dnxhd, mpeg2video, vc1, …) are enabled in the build but
  need a licence-clean media corpus to exercise end-to-end (spec 0016 §8) — the codecs with an
  encoder are verified by an encode→decode round-trip.

## Full (native only, + over intermediate)

The **heavy next-generation encoders** (spec 0023). They are thread- and SIMD-hungry — impractical
in WASM — so they ship **only in the native driver's `full` profile**
(`ffmpeg-wasi-driver-linux-amd64-full-<variant>`, spec 0028), never in any `.wasm`.

| | Encode (name) | Produces | Variant | Notes |
|---|---|---|---|---|
| **AV1** | `libsvtav1` | av1 | both | SVT-AV1 (BSD); **royalty-free**; `preset` 0–13 (higher = faster) |
| **HEVC/H.265** | `libx265` | hevc | **gpl only** | libx265 (GPL); **active HEVC patent pool** — see [licensing](../explanation/licensing.md) |

HEVC **decode** needs no full profile — the native `hevc` decoder is in every build (lean and up).
HW-accel encoders (NVENC/VAAPI/…) are the remaining full members, deferred until a GPU is available.
