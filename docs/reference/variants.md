---
title: Variants & artifacts
description: The LGPL and GPL builds, the codec/filter/muxer baseline, and the release artifacts.
date: 2026-06-28
tags: [reference, releases]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Variants & artifacts

## Profiles (capability classes)

A build has two axes: a **licence variant** (LGPL/GPL, below) and a **profile** — a capability
class from [afmpeg spec 0022](https://afmpeg.phpboyscout.uk/development/specs/0022-build-size-matrix/):

- **`lean`** (default) — web-delivery essentials at the smallest size. This is the baseline
  documented below, and what today's `ffmpeg-wasi-<variant>.wasm` releases are.
- **`intermediate`** — lean **+ every practical software codec/format/filter** (no hardware, no
  heavy encoders), filled additively by the codec-coverage specs. It currently adds the **native
  container batch** (spec 0015): MPEG-TS, HLS/DASH segmenting, fragmented MP4/CMAF, FLV, AVI,
  animated GIF, and the audio containers (ADTS/CAF/AIFF/AU); and the **native filter batch**
  (spec 0017): colour/levels, compose (`hstack`/`blend`), frame select (`select`/`thumbnail`),
  `palettegen`/`paletteuse`, deinterlace (`yadif`), `loudnorm`/`atempo`, and more — see
  [filters](filters.md); and the **native codec batch** (spec 0016): AC-3/E-AC-3/DTS decode,
  ProRes/DNxHD/DV, MPEG-2/4/VC-1/Theora, ALAC, the PCM family, and BMP/TIFF/GIF — see
  [codecs](codecs.md); the **external LGPL encoder libs** (spec 0018), cross-compiled into
  the build via `build/deps.sh`: **Opus** (`libopus`), **MP3** (`libmp3lame`), **Vorbis**
  (`libvorbis`), **VP8/VP9** (`libvpx`) and **WebP** (`libwebp`); and the **text/subtitle burn-in**
  libs (spec 0019): **freetype** + **harfbuzz** (the `drawtext` filter) and **libass** (the
  `subtitles`/`ass` filters), the meson-built ones via spec 0029. Its artifact is named
  `ffmpeg-wasi-intermediate-<variant>.wasm`.
- **`full`** — intermediate **+ the heavy native-only encoders** (spec 0023): **AV1** via
  [SVT-AV1](https://gitlab.com/AOMediaCodec/SVT-AV1) (`libsvtav1`, both variants — BSD, royalty-free)
  and **HEVC/H.265** via [libx265](https://bitbucket.org/multicoreware/x265_git) (`libx265`, **gpl
  variant only** — GPL + an active HEVC patent pool, see [licensing](../explanation/licensing.md)).
  These are thread- and SIMD-hungry, so **full is native-only** — there is no WASM full module; it
  ships as the native driver `ffmpeg-wasi-driver-linux-amd64-full-<variant>` (spec 0028). Hardware
  encoders (NVENC/VAAPI/…) are the remaining full members, deferred until a GPU is available.

Build a profile locally with the `PROFILE` build-arg (or `just build <variant> <profile>`); the WASM
build accepts `lean`/`intermediate`, the native driver (`build/Dockerfile.native`) accepts all three:

```sh
docker build -f build/Dockerfile --build-arg VARIANT=lgpl --build-arg PROFILE=intermediate \
  --target artifact -o dist .
# the native full driver (HEVC via x265 needs the gpl variant):
docker build -f build/Dockerfile.native --build-arg VARIANT=gpl --build-arg PROFILE=full \
  --target artifact -o dist-native .
```

The load-bearing guarantee (0022 D-0022-B): **intermediate is identical across the WASM and the
native runtimes** — the same codec set, so a consumer moves between them with no capability change,
only a performance/security-posture shift. Full then extends the native runtime past what WASM can do.

## The two variants

| | `ffmpeg-wasi-lgpl.wasm` | `ffmpeg-wasi-gpl.wasm` |
|---|---|---|
| **Licence** | LGPL-2.1-or-later | GPL-2.0-or-later |
| **`--enable-gpl`** | no | yes |
| **H.264 encode** | openh264 (BSD) | libx264 (best quality) |
| **Use when** | you want proprietary compatibility | you want x264 and accept GPL |

Both ship in every release — [pick the one that fits](../how-to/choose-a-variant.md) and skip
building. See [the licensing model](../explanation/licensing.md) for why shipping both is
clean. Both encode H.264; the LGPL variant's self-compiled openh264 carries an AVC **patent**
caveat — [licensing](../explanation/licensing.md#h264-encode-and-the-avc-patent-pool).

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

Releases are published [here](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases); the
first is [`n8.1.2-1`](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-1). Each
release (`nX.Y.Z-N`) publishes:

| Asset | What |
|---|---|
| `ffmpeg-wasi-lgpl.wasm` / `.gz` | the LGPL **lean** module (and gzipped) |
| `ffmpeg-wasi-gpl.wasm` / `.gz` | the GPL **lean** module (and gzipped) |
| `ffmpeg-wasi-intermediate-lgpl.wasm` / `.gz` | the LGPL **intermediate** module — lean + subtitles, LGPL encoders, native codec batch, burn-in |
| `ffmpeg-wasi-intermediate-gpl.wasm` / `.gz` | the GPL **intermediate** module |
| `ffmpeg-wasi-driver-linux-amd64-{lgpl,gpl}` / `.gz` | the **native Backend-B driver** (linux/amd64), lean profile — threads + SIMD, 48–58× faster software encode; driven by afmpeg's native backend (spec 0028) |
| `ffmpeg-wasi-driver-linux-amd64-intermediate-{lgpl,gpl}` / `.gz` | the native driver, **intermediate** profile — the full software-codec batch at native speed |
| `ffmpeg-wasi-driver-linux-amd64-full-{lgpl,gpl}` / `.gz` | the native driver, **full** profile — intermediate + AV1 (SVT-AV1) + HEVC (x265, gpl only) |
| `checksums.txt` | SHA-256 of every artifact (incl. `provenance.json`) — [verify before use](../how-to/choose-a-variant.md) |
| `checksums.txt.sig` | a **detached OpenPGP signature** over `checksums.txt` from a release-signing key held in AWS KMS, signable only by this project's tag pipeline (GitLab OIDC). [afmpeg](https://gitlab.com/phpboyscout/afmpeg) verifies it (via `gitlab.com/phpboyscout/signing`) against a pinned key |
| `provenance.json` | the exact FFmpeg version, build tag, and commit, plus a per-variant record (file, licence, H.264 encoder, profile) |

Because the signature covers `checksums.txt`, and `checksums.txt` covers every other
asset (including `provenance.json`), one signature certifies the whole release. The private
key never leaves KMS and no human can wield it — only the tagged-release CI job can sign.

## Versioning

A tag is **`<FFMPEG_VERSION>-<build-rev>`**, e.g. `n8.1.2-1`. The FFmpeg version tracks
upstream; the build revision bumps when the toolchain or build configuration changes for the
same upstream FFmpeg. Pin a consumer to a specific tag + the published SHA-256.
