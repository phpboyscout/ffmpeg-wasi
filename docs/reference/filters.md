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
| **Text / subtitles** (spec [0019](https://afmpeg.phpboyscout.uk/development/specs/0019-text-and-subtitles/)) | `drawtext` (freetype + harfbuzz), `subtitles`, `ass` (libass) — burn text/`.srt`/`.ass` onto video |

### Notes

- **`palettegen`/`paletteuse`** run as a single-pass split graph — pair them for a good GIF:
  `[0:v]split[a][b];[a]palettegen[p];[b][p]paletteuse[v]`.
- **`loudnorm`** resamples to 192 kHz internally; follow it with `aresample=<rate>` before an
  encoder that doesn't accept that rate (e.g. AAC).
- **Analysis filters** (`cropdetect`, `blackdetect`, `silencedetect`, `ebur128`, `signalstats`,
  `astats`, …) surface their measurements as **structured output** (spec 0017 §Q): the `lavfi.*`
  metadata they attach to frames is collected into the process result JSON's **`analysis`** array
  — a time-series of `{t, key, value}`, deduplicated per key (a stable `cropdetect` records once;
  discrete events like `silence_start`/`silence_end` each record). Filters that only log by default
  (`ebur128`, `astats`) need their `metadata=1` option to populate it. afmpeg exposes it as
  `ProcessResult.Analysis` via `ParseResult`.
- **`eq`** (a GPL-only level/gamma filter) is **not enabled in either variant** — reach for
  `curves`, `colorbalance`, or `colorchannelmixer` for level adjustment instead.
- **Fonts have no fontconfig** — the sandbox has no system fonts. Name the font by path:
  `drawtext=fontfile=/font.ttf:…`; for `subtitles`/`ass`, point libass at a mounted directory
  (`subtitles=filename=sub.srt:fontsdir=/fonts:force_style='FontName=…'`). The libs
  (freetype/harfbuzz/libass) cross-compile via the meson toolchain of spec 0029.

## Full (native)

The native-only **full** profile ([0022](https://afmpeg.phpboyscout.uk/development/specs/0022-build-size-matrix/))
adds the heavy HEVC/AV1 *encoders* (see [codecs](codecs.md)) but **no new filters** — the filter
set is identical to intermediate.
