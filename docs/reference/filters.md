---
title: Filters
description: The libavfilter filters enabled per build profile — the vocabulary available inside the job-spec `filter` string.
date: 2026-07-03
tags: [reference, filters]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Filters

The job-spec [`filter`](job-spec.md) field is an arbitrary ffmpeg filtergraph string parsed by
`avfilter_graph_parse2`. A filter can be used only if it is **enabled in the build** — this is
the authoritative list of what each [profile](variants.md) links.

## Lean (all builds)

The web-delivery essentials:

`null`, `anull`, `split`, `asplit`, `scale`, `crop`, `pad`, `format`, `fps`, `settb`, `asettb`,
`setsar`, `setpts`, `asetpts`, `trim`, `atrim`, `loop`, `transpose`, `overlay`, `concat`,
`xfade`, `amix`, `adelay`, `volume`, `afade`, `aresample`, `aformat`, `alimiter`.

## Intermediate (+ over lean)

The native filter batch (spec [0017](https://afmpeg.phpboyscout.uk/development/specs/0017-native-filter-batch/)) —
all in-tree, LGPL-clean, no external library:

| Group | Filters |
|---|---|
| **Video fade** | `fade` |
| **Colour / levels** | `hue`, `colorbalance`, `curves`, `colorchannelmixer`, `lut`, `lut3d` |
| **Sharpen / blur** | `unsharp`, `gblur`, `boxblur` |
| **Compose / grid** | `hstack`, `vstack`, `xstack`, `blend`, `tile` |
| **Frame select** | `select`, `aselect`, `thumbnail`, `framestep` |
| **GIF quality** | `palettegen`, `paletteuse` |
| **Deinterlace** | `yadif`, `bwdif` |
| **Keying** | `chromakey`, `colorkey` |
| **Geometry** | `rotate`, `hflip`, `vflip`, `reverse`, `areverse` |
| **Draw (native)** | `drawbox`, `drawgrid`, `vignette` |
| **Audio loudness** | `loudnorm`, `dynaudnorm`, `acompressor`, `compand` |
| **Audio EQ / dynamics** | `highpass`, `lowpass`, `equalizer`, `atempo`, `aecho`, `silenceremove`, `afftdn` |
| **Channels** | `pan`, `channelsplit`, `channelmap`, `join` |
| **Analysis (metadata)** | `cropdetect`, `blackdetect`, `signalstats`, `silencedetect`, `ebur128`, `astats` |

### Notes

- **`palettegen`/`paletteuse`** run as a single-pass split graph — pair them for a good GIF:
  `[0:v]split[a][b];[a]palettegen[p];[b][p]paletteuse[v]`.
- **`loudnorm`** resamples to 192 kHz internally; follow it with `aresample=<rate>` before an
  encoder that doesn't accept that rate (e.g. AAC).
- **Analysis filters** (`cropdetect`, `ebur128`, …) currently emit their measurements to the log
  (stderr), not as structured output — observe-only for now (spec 0017 Q-0017-1).
- **`eq`** is a GPL filter, so it is present only in the **gpl** variant, not the LGPL default.
- **`drawtext`** (freetype) and **`subtitles`/`ass`** (libass) pull external libraries and are
  **not** in this batch — they belong to spec 0019.
