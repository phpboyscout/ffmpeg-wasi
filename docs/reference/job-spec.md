---
title: The job-spec vocabulary
description: The structured operations the engine accepts — process and probe — the compatibility contract with afmpeg.
date: 2026-06-28
tags: [reference, api]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# The job-spec vocabulary

The engine is driven by a **structured job spec**, not `ffmpeg` command-line strings. This is
a deliberate choice ([Why libav-direct](../explanation/why-libav-direct.md)): a typed surface
that is *exactly* as capable as the engine, with no leaky partial-CLI illusion. The vocabulary
is the **compatibility contract** between ffmpeg-wasi and [afmpeg](https://gitlab.com/phpboyscout/afmpeg);
it is versioned, and afmpeg pins a known-good engine + vocabulary version.

!!! note "Status"
    **`probe` and `process` both run today.** `process` supports **multiple inputs, the full
    `filter_complex`, and multiple output files** — pad labels (`[0:v]`, `[1:a]`, … →
    `[vout]`, `[aout]`) parsed by `avfilter_graph_parse2`; each graph output pad is routed by
    `map` to the `outputs[]` entry that names it, encoded (video pads with `video_codec`,
    audio pads with `audio_codec`) and muxed into that file. With no `filter`, a passthrough
    graph is generated for input 0. Shapes follow afmpeg
    [spec 0007 §4](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0007-libav-direct-engine.md).

## Operations

### `process` — transcode / filter / mux

```jsonc
{
  "op": "process",
  "inputs":  [ { "path": "in/clip.mp4", "options": { } } ],
  "filter":  "[0:v]scale=1280:-2[v]",   // an ffmpeg filtergraph STRING (see below)
  "outputs": [ {
    "path": "out/clip.mp4",
    "map": ["[v]"],                     // graph pads / stream specifiers to mux
    "video_codec": "libx264",
    "audio_codec": "aac",
    "options": { "crf": "23", "movflags": "+faststart" }
  } ]
}
```

| Field | Meaning |
|---|---|
| `version` | The job-spec **vocabulary version** the spec is written in (integer; absent == 0, the pre-gate baseline). The engine rejects a spec whose `version` exceeds what it supports — see [Versioning](#versioning). |
| `inputs[]` | Each input's path (resolved against the mounted filesystem) + demuxer options. |
| `inputs[].concat` | *(v2)* An array of like-codec file paths joined into one continuous input via the **concat demuxer** (a stream-copy join; distinct from the `concat` filter, which re-encodes). When set, replaces `path`. |
| `filter` | The full ffmpeg `filter_complex` string — `[0:v]scale=…[vout];[1:a]…[aout]`. Optional (passthrough graph for input 0 if omitted). |
| `outputs[]` | One **output file** each. With a single output, `map` may be omitted (every graph pad is muxed into it); with **multiple outputs, each must set `map`** to claim its pads. |
| `outputs[].map` | What to mux into this file — either graph output pad labels (bracketed, `["[loud]"]`, encoded) **or** *(v2)* input-stream specifiers (unbracketed: `"0:v"`, `"0:a:0"`, `"0:0"`, **stream-copied**). |
| `outputs[].video_codec` / `audio_codec` | The encoder for that media type, by name (e.g. `libx264`, `aac`). *(v2)* The sentinel **`"copy"`** stream-copies the mapped input stream — no decode/encode, needs no codec, works for any codec in either variant. The output container is chosen from the path extension. |
| `outputs[].options` | String key/values passed to the encoder (e.g. `{"crf":"28"}`). |
| `outputs[].bitstream_filters` | *(v2)* Per copied stream, keyed by its `map` entry: a bitstream-filter name/chain (e.g. `{"0:v":"h264_mp4toannexb"}`), or `"none"` to force-disable. Absent → the muxer auto-inserts any container-required filter. |

Working examples (verified end-to-end):

```jsonc
// audio: WAV (pcm) → AAC in MP4 (no filter → passthrough graph)
{"op":"process","inputs":[{"path":"tone.wav"}],
 "outputs":[{"path":"out.mp4","audio_codec":"aac"}]}

// video: H.264 → scaled → H.264 (GPL variant, libx264)
{"op":"process","inputs":[{"path":"in.mp4"}],"filter":"[0:v]scale=160:120[vout]",
 "outputs":[{"path":"out.mp4","video_codec":"libx264","options":{"crf":"28"}}]}

// multi-input: combine a video + an audio file into one mp4
{"op":"process","inputs":[{"path":"clip.mp4"},{"path":"music.mp3"}],
 "filter":"[0:v]scale=1280:-2[vout];[1:a]anull[aout]",
 "outputs":[{"path":"out.mp4","video_codec":"libx264","audio_codec":"aac"}]}

// crossfade-concat two clips
{"op":"process","inputs":[{"path":"a.mp4"},{"path":"b.mp4"}],
 "filter":"[0:v][1:v]xfade=transition=fade:duration=0.4:offset=2[vout]",
 "outputs":[{"path":"out.mp4","video_codec":"libx264"}]}

// multi-output: split one source (asplit) into two files via `map`
{"op":"process","inputs":[{"path":"tone.wav"}],
 "filter":"[0:a]asplit=2[a1][a2];[a1]volume=0.9[loud];[a2]volume=0.1[quiet]",
 "outputs":[{"path":"loud.mp4","map":["[loud]"],"audio_codec":"aac"},
            {"path":"quiet.mp4","map":["[quiet]"],"audio_codec":"aac"}]}

// v2 — remux, no re-encode (both streams stream-copied)
{"op":"process","version":2,"inputs":[{"path":"in.mp4"}],
 "outputs":[{"path":"out.mkv","map":["0:v","0:a"],"video_codec":"copy","audio_codec":"copy"}]}

// v2 — mixed: copy the video, re-encode only the audio
{"op":"process","version":2,"inputs":[{"path":"in.mp4"}],"filter":"[0:a]loudnorm[aout]",
 "outputs":[{"path":"out.mp4","map":["0:v","[aout]"],"video_codec":"copy","audio_codec":"aac"}]}

// v2 — stream-copy join of like-codec segments via the concat demuxer
{"op":"process","version":2,"inputs":[{"concat":["a.ts","b.ts","c.ts"]}],
 "outputs":[{"path":"joined.ts","map":["0:v","0:a"],"video_codec":"copy","audio_codec":"copy"}]}
```

On success the engine prints what it wrote, one entry per output file, e.g.
`{"outputs":[{"path":"loud.mp4","streams":[{"type":"audio","codec":"aac"}]},{"path":"quiet.mp4","streams":[{"type":"audio","codec":"aac"}]}]}`.

### `probe` — report stream information

```jsonc
{ "op": "probe", "inputs": [ { "path": "in/clip.mp4" } ] }
```

Reports container/stream metadata (format, duration, per-stream codec/type and
dimensions/sample rate) as JSON on stdout. No outputs are written. For example, probing a
WAV yields:

```json
{"inputs":[{"path":"tone.wav","format":"wav","duration_sec":0.5,
  "streams":[{"index":0,"type":"audio","codec":"pcm_s16le","sample_rate":8000,"channels":1}]}]}
```

### `version` — report the vocabulary version

```jsonc
{ "op": "version" }
```

Prints the engine's highest supported vocabulary version + its FFmpeg build as JSON on
stdout — the machine-readable channel a consumer preflights before running jobs. Needs no
input, so it works before any media is mounted:

```json
{"vocab_version":1,"ffmpeg_version":"n8.1.2"}
```

This query carries no `version` of its own, so even an engine older than the consumer answers
it (it is the negotiation channel, exempt from the gate).

## The `filter` field is ffmpeg's filtergraph syntax

The one place we deliberately **don't** invent our own language. libavfilter ships a complete
graph parser; reinventing the `[0:v]scale=…[v]` mini-language would be folly, and your
existing filtergraph knowledge transfers directly. Structured fields surround the graph
(inputs, outputs, codecs, options); the graph itself is the standard string.

## Transport

The spec is passed to the engine as a single argument (or read from the mounted filesystem).
Results — the probe JSON, or process status — come back on stdout; errors on stderr with a
non-zero exit code. afmpeg's runtime carries all of this over its filesystem bridge.

## Versioning

The vocabulary carries a **version** that increments additively — once per landed vocabulary
spec, in merge order — so a new field never has to be guessed at by an older engine:

| Version | Adds |
|---|---|
| 0 | The pre-gate baseline (no `version` field). |
| 1 | Baseline + the version gate (`op:version`); no process/probe field changes. |
| 2 | Stream copy / bitstream filters / concat demuxer (afmpeg spec 0013): the `copy` codec sentinel, unbracketed `in:type[:idx]` map specifiers, `outputs[].bitstream_filters`, `inputs[].concat`. |

**The gate (two sides):**

- **Engine (runtime).** Every `process`/`probe` spec is stamped by the consumer with the
  `version` it was written in. If that exceeds the engine's `AFMPEG_VOCAB_VERSION`, the engine
  rejects the whole spec — stderr message + a **distinct exit code `3`** (`version-too-new`, so a
  caller can tell "upgrade the engine" from the malformed-spec code `2`) — rather than silently
  dropping the fields it doesn't understand.
- **Consumer (preflight).** afmpeg reads `op:"version"` once at construction and fails loudly if
  the module is a gated engine older than the vocabulary it emits, turning a would-be silent
  field-drop at first job into a clear startup error. A module that doesn't answer `op:"version"`
  (a pre-gate engine, or a generic non-ffmpeg-wasi module) is tolerated — it carries no
  vocabulary contract to check.

The engine's current version is in the `--report` output and via `op:"version"`.
