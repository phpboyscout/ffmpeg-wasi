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

### Notes

- **DTS is decode-only** (`dca`) — the encoder is experimental in FFmpeg and left out
  (D-0016-B). TrueHD/AMR are not in this batch.
- **`eq`** is not a codec — see [filters](filters.md).
- **HEVC/AV1 encode and AV1 decode (dav1d)** are *not* here — they belong to spec 0023.
- **Patent posture** — AC-3, DTS, MPEG-2/4, VC-1 carry codec patents, the same class as the
  H.264/HEVC the engine already decodes; see the [licensing explanation](../explanation/licensing.md).
- The **decode-only** codecs (prores, dnxhd, mpeg2video, vc1, …) are enabled in the build but
  need a licence-clean media corpus to exercise end-to-end (spec 0016 §8) — the codecs with an
  encoder are verified by an encode→decode round-trip.
