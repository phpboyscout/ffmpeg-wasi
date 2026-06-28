---
title: Variants & artifacts
description: The LGPL and GPL builds, the codec/filter/muxer baseline, and the release artifacts.
date: 2026-06-28
tags: [reference, releases]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Variants & artifacts

## The two variants

| | `ffmpeg-wasi-lgpl.wasm` | `ffmpeg-wasi-gpl.wasm` |
|---|---|---|
| **Licence** | LGPL-2.1-or-later | GPL-2.0-or-later |
| **`--enable-gpl`** | no | yes |
| **H.264 encode** | openh264 (or none) | libx264 (best quality) |
| **Use when** | you want proprietary compatibility | you want x264 and accept GPL |

Both ship in every release — [pick the one that fits](../how-to/choose-a-variant.md) and skip
building. See [the licensing model](../explanation/licensing.md) for why shipping both is
clean.

## The media baseline

The enabled codecs/filters/muxers are a **general** baseline (not one consumer's set). The
exact matrix is recorded per release in the provenance manifest; the intended baseline:

- **Decode** — H.264, HEVC, VP8/VP9, MJPEG, AAC, MP3, Opus, Vorbis, FLAC, PCM, and the common
  image formats.
- **Encode** — H.264 (per variant), AAC, MJPEG, FLAC, PCM (plus image and audio encoders as
  dependencies land).
- **Demux / mux** — MP4/MOV, Matroska/WebM, MP3, WAV, Ogg, image sequences.
- **Filters** — `scale`, `crop`, `pad`, `overlay`, `concat`, `xfade`, `format`, `fps`, plus
  the common audio filters (`amix`, `volume`, `afade`, `aresample`, …).
- **Protocols** — `file` (over the mounted filesystem).

External-dependency codecs (libx264, openh264, zlib for PNG, …) are added incrementally; each
build's true set is authoritative in its provenance manifest.

## Release artifacts

Each release (`nX.Y.Z-N`) publishes:

| Asset | What |
|---|---|
| `ffmpeg-wasi-lgpl.wasm` / `.gz` | the LGPL module (and gzipped) |
| `ffmpeg-wasi-gpl.wasm` / `.gz` | the GPL module (and gzipped) |
| `checksums.txt` | SHA-256 of every artifact — [verify before use](../how-to/choose-a-variant.md) |
| `provenance.json` | exact FFmpeg/dependency/toolchain versions, configure line, per-asset licence |

## Versioning

A tag is **`<FFMPEG_VERSION>-<build-rev>`**, e.g. `n8.1.2-1`. The FFmpeg version tracks
upstream; the build revision bumps when the toolchain or build configuration changes for the
same upstream FFmpeg. Pin a consumer to a specific tag + the published SHA-256.
