---
title: Containers, bitstream filters & protocols
description: The demuxers and muxers enabled per build profile, the bitstream filters available to a stream copy, and the two protocols the engine can open.
date: 2026-08-02
tags: [reference, containers, formats]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Containers, bitstream filters & protocols

What the engine can **read** (demuxers), **write** (muxers), and rewrite in passing (bitstream
filters), by [profile](variants.md). This is the container half of the picture;
[codecs](codecs.md) is the codec half and [filters](filters.md) the filtergraph half.

The build starts from `--disable-everything` and enables an explicit allowlist, so **a format not
listed here is not present** — the muxer cannot be resolved and the job fails with
`process: cannot resolve output format`.

## How a container is chosen

- **Reading.** libavformat probes the input. Set `inputs[].format` to force a demuxer by name
  instead — required for headerless input such as `rawvideo` or raw PCM, where there is nothing to
  probe. See [the job spec](job-spec.md#operations).
- **Writing.** The muxer is guessed from the output path's extension. Set `outputs[].format` to
  force one by name — required wherever the extension does not imply it (`hls`, `dash`, `segment`)
  or where you want a different muxer than the extension suggests.
- **Muxer options** go in `outputs[].format_options`, never in `outputs[].options` (which reaches
  the *encoder*). Segment timing and naming, fragmentation flags and playlist settings are all
  muxer options.

## Lean (all builds)

| | Formats |
|---|---|
| **Demux** | `mov` (MP4/MOV/M4A), `matroska`, `webm`, `mp3`, `wav`, `ogg`, `aac`, `flac`, `image2`, `concat`, `rawvideo`, `pcm_s16le`, `pcm_f32le` |
| **Mux** | `mp4`, `mov`, `matroska`, `webm`, `mp3`, `wav`, `image2` |

The asymmetry is deliberate: `lean` **reads** Ogg, AAC-in-ADTS, FLAC and raw streams but **writes**
only the seven muxers above. Writing an `.ogg`, `.aac` or `.flac` file needs the `intermediate`
profile.

`concat` is the demuxer behind [`inputs[].concat`](job-spec.md#operations) — a stream-copy join of
like-codec files. It is present in every profile.

## Intermediate (+ over lean)

The native container batch, all in-tree and LGPL-clean, no external library:

| Group | Demux | Mux |
|---|---|---|
| **Broadcast / streaming** | `mpegts` | `mpegts` |
| **Adaptive segmenting** | — | `hls`, `dash`, `segment`, `stream_segment` |
| **Legacy web / editing** | `flv`, `avi` | `flv`, `avi` |
| **Animated images** | `gif` | `gif` |
| **Audio containers** | `caf`, `aiff`, `au` | `ogg`, `adts`, `caf`, `aiff`, `au` |
| **Subtitle sidecars** | `srt`, `ass`, `webvtt` | `srt`, `webvtt`, `ass` |

Fragmented MP4 / CMAF is not a separate muxer — it is the `mp4` muxer driven with
`format_options: {"movflags": "+frag_keyframe+empty_moov"}`, available in every profile.

### Segmenting outputs write a set of files

`hls`, `dash`, `segment` and `stream_segment` write a playlist or manifest **plus** the segment
files beside it on the mounted filesystem. The output's `path` is the playlist; the segments follow
whatever naming pattern you set in `format_options`. Such an output reports `"segmented": true` in
its result entry rather than listing a single file.

```jsonc
{"op":"process","version":5,"inputs":[{"path":"in.mp4"}],"filter":"[0:v]null[v]",
 "outputs":[{"path":"stream.m3u8","map":["[v]"],"format":"hls","video_codec":"libopenh264",
   "format_options":{"hls_time":"4","hls_segment_filename":"seg_%03d.ts","hls_list_size":"0"}}]}
```

Nothing is uploaded. HLS and DASH here mean *writing the files*; serving them is the host's job —
the engine has no network ([why](limits.md#can-an-input-or-output-be-a-url)).

## Full (native)

The `full` profile adds no containers. Its additions are the heavy HEVC and AV1
[encoders](codecs.md#full-native-only-over-intermediate); the format set is identical to
`intermediate`.

## Bitstream filters

The same four in **every** profile — a stream copy sometimes needs a container-specific rewrite of
the bitstream even though nothing is decoded:

| BSF | What it does |
|---|---|
| `h264_mp4toannexb` | H.264 length-prefixed (MP4) → Annex-B start codes, for MPEG-TS |
| `hevc_mp4toannexb` | the same for HEVC |
| `aac_adtstoasc` | AAC ADTS headers → the ASC form MP4/MOV wants |
| `extract_extradata` | lifts codec extradata out of the packet stream |

**You rarely need to name one.** The muxer auto-inserts whichever the container requires, so an
MP4 → MPEG-TS remux picks up `h264_mp4toannexb` on its own. Use `outputs[].bitstream_filters` to
force a specific chain, or the value `"none"` to suppress the automatic one:

```jsonc
{"outputs":[{"path":"out.ts","map":["0:v","0:a"],
  "video_codec":"copy","audio_codec":"copy",
  "bitstream_filters":{"0:v":"h264_mp4toannexb"}}]}
```

Naming a BSF this build does not carry fails with `process: bad bitstream filter`.

## Protocols

Two, in every profile:

| Protocol | Use |
|---|---|
| `file` | every media path in a job spec, resolved against the mounted filesystem |
| `pipe` | libav's internal pipe protocol |

`libav*` is built `--disable-network`, so **no network protocol exists** — no `http`, `https`,
`rtmp`, `srt`, `rtsp` or `tcp`. A URL is not an openable path; see
[limits](limits.md#can-an-input-or-output-be-a-url).

## Related

- [Codecs](codecs.md) — what can be decoded and encoded inside these containers.
- [The job-spec vocabulary](job-spec.md) — `inputs[].format`, `outputs[].format`,
  `format_options` and `bitstream_filters`.
- [Build options](build-options.md) — where the allowlist above is defined.
