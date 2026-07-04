---
title: The job-spec vocabulary
description: The structured operations the engine accepts — process, probe, frames, and version — the compatibility contract with afmpeg.
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
    **`probe`, `process`, `frames`, and `version` all run today.** `process` supports **multiple inputs, the full
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
| `inputs[].seek` | *(v3)* `{"start": seconds, "mode": "fast"\|"accurate"}` — start the input at a point instead of decoding from the beginning. **fast** (default) seeks the demuxer to the keyframe at-or-before `start`; **accurate** additionally decodes-and-discards to the exact frame. Accurate cannot feed a copied stream (a copy cuts on keyframes) — that is a hard error. |
| `inputs[].format` | *(v4)* Force the demuxer by name (e.g. `"rawvideo"`, `"s16le"`, `"mp4"`) instead of auto-probing. Required for headerless/raw inputs. |
| `inputs[].options` | *(v4)* Demuxer options passed as an AVDictionary — raw geometry rides here (`{"video_size":"1280x720","pixel_format":"yuv420p","framerate":"25"}` for rawvideo; `{"sample_rate":"48000","ch_layout":"mono"}` for PCM). An unconsumed key is a typed error. |
| `filter` | The full ffmpeg `filter_complex` string — `[0:v]scale=…[vout];[1:a]…[aout]`. Optional (passthrough graph for input 0 if omitted). |
| `outputs[]` | One **output file** each. With a single output, `map` may be omitted (every graph pad is muxed into it); with **multiple outputs, each must set `map`** to claim its pads. |
| `outputs[].map` | What to mux into this file — either graph output pad labels (bracketed, `["[loud]"]`, encoded) **or** *(v2)* input-stream specifiers (unbracketed: `"0:v"`, `"0:a:0"`, `"0:0"`, **stream-copied**). Graph input pads accept the same indexed form — `[0:v:1]` in a `filter` selects the second video stream *(v4)*. |
| `outputs[].video_codec` / `audio_codec` | The encoder for that media type, by name (e.g. `libx264`, `aac`). *(v2)* The sentinel **`"copy"`** stream-copies the mapped input stream — no decode/encode, needs no codec, works for any codec in either variant. The output container is chosen from the path extension. |
| `outputs[].options` | String key/values passed to the **encoder** (e.g. `{"crf":"28"}`). |
| `outputs[].format` | *(v5)* Force the **muxer** by name (`"hls"`, `"dash"`, `"segment"`, `"mpegts"`, …) instead of guessing from the path extension. |
| `outputs[].format_options` | *(v5)* Options passed to the **muxer** (write_header) — segment timing/naming (`hls_time`, `hls_segment_filename`), fragmentation (`movflags`), etc. Distinct from `options` (the encoder). |
| `outputs[].bitstream_filters` | *(v2)* Per copied stream, keyed by its `map` entry: a bitstream-filter name/chain (e.g. `{"0:v":"h264_mp4toannexb"}`), or `"none"` to force-disable. Absent → the muxer auto-inserts any container-required filter. |
| `outputs[].duration` / `outputs[].end` | *(v3)* Stop the output after `duration` seconds (`-t`) or at position `end` (`-to`). **Mutually exclusive.** On the default zero-based timeline the two coincide; under `copy_ts`, `end` is an absolute source position. |
| `outputs[].copy_ts` | *(v3)* `true` preserves source timestamps. Default `false` zero-bases the output — a fast-seeked clip starts at the keyframe actually landed on, an accurate one at the requested start. |

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

// v3 — clip extraction: 5s from 0:12.5, frame-accurate, re-encoded
{"op":"process","version":3,
 "inputs":[{"path":"in.mp4","seek":{"start":12.5,"mode":"accurate"}}],
 "filter":"[0:v]null[v]",
 "outputs":[{"path":"clip.mp4","map":["[v]"],"video_codec":"libopenh264","duration":5.0}]}

// v3 — keyframe-accurate copy-trim: cheap cut, no re-encode (fast seek + copy)
{"op":"process","version":3,
 "inputs":[{"path":"in.mp4","seek":{"start":12.5}}],
 "outputs":[{"path":"cut.mp4","map":["0:v","0:a"],"video_codec":"copy","audio_codec":"copy"}]}

// v4 — raw headerless input: forced demuxer + geometry, transcoded
{"op":"process","version":4,
 "inputs":[{"path":"frames.yuv","format":"rawvideo",
            "options":{"video_size":"1280x720","pixel_format":"yuv420p","framerate":"25"}}],
 "filter":"[0:v]null[v]",
 "outputs":[{"path":"out.mp4","map":["[v]"],"video_codec":"libopenh264"}]}

// v5 — mp4 → MPEG-TS remux, no re-encode (the h264_mp4toannexb BSF auto-inserts)
{"op":"process","version":5,"inputs":[{"path":"in.mp4"}],
 "outputs":[{"path":"out.ts","map":["0:v","0:a"],"video_codec":"copy","audio_codec":"copy"}]}

// v5 — HLS: one output entry writes segment files + a playlist to the fs (no network)
{"op":"process","version":5,"inputs":[{"path":"in.mp4"}],"filter":"[0:v]null[v]",
 "outputs":[{"path":"stream.m3u8","map":["[v]"],"format":"hls","video_codec":"libopenh264",
   "format_options":{"hls_time":"4","hls_segment_filename":"seg_%03d.ts","hls_list_size":"0"}}]}

// v7 — remux to mkv, set container tags, copy chapters, tag a stream's language/disposition
{"op":"process","version":7,"inputs":[{"path":"in.mp4"}],
 "outputs":[{"path":"out.mkv","map":["0:v","0:a"],"video_codec":"copy","audio_codec":"copy",
   "metadata":{"title":"My Title","artist":"Me"},
   "chapters":"copy",
   "stream_metadata":{"0:a":{"language":"eng","disposition":["default"]}}}]}

// v8 — convert a subtitle track to WebVTT (sidecar), and embed one as mov_text in mp4
{"op":"process","version":8,"inputs":[{"path":"sub.srt"}],
 "outputs":[{"path":"out.vtt","map":["0:s"],"subtitle_codec":"webvtt"}]}
{"op":"process","version":8,"inputs":[{"path":"in.mp4"},{"path":"sub.srt"}],
 "outputs":[{"path":"out.mp4","map":["0:v","0:a","1:s"],
   "video_codec":"copy","audio_codec":"copy","subtitle_codec":"mov_text"}]}
```

**Subtitle streams (spec 0019).** `outputs[].subtitle_codec` transcodes a subtitle track named in
`map` by an `N:s` specifier (e.g. `srt`, `webvtt`, `mov_text`, `ass`), or `"copy"` to remux it
unchanged. An output may carry `subtitle_codec` alone (a sidecar `.srt`/`.vtt`) or beside video +
audio (an embedded track). Subtitles ride their own decode→encode lane — they do not traverse the
`filter` graph — so they are mapped by stream specifier, never a graph-pad label.

**Metadata (spec 0020).** `outputs[].metadata` is a `{key:value}` tag map set on the output
container; `outputs[].chapters` is a passthrough directive (`"copy"` carries the first input's
chapters, an input index picks another, `"none"`/absent drops them); `outputs[].stream_metadata`
maps a `map` entry to `{language, disposition, tags}` applied to that output stream. An
`attached_pic` cover-art stream copied via `map` keeps its disposition automatically.

A **segmenting** output (`hls`/`dash`/`segment`) reports `"segmented": true` in its result entry;
`path` is the playlist/manifest and the segment files sit beside it on the mounted filesystem by
the requested pattern.

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

Since **v7** (spec 0020) the reply additionally carries, where present, the container's `tags`
object and `chapters` array (`start`/`end` in seconds + `title`), and on each stream its
`language`, decoded `disposition` flag names, and `tags` — all additive, so an older consumer
ignores them.

### `frames` — extract still frames

```jsonc
{
  "op": "frames", "version": 6,
  "inputs": [ { "path": "in/clip.mp4" } ],   // exactly one video input
  "select": {                                 // exactly one selector
    "timestamp":  12.5,                        // (a) single frame, seconds
    "timestamps": [1.0, 5.0, 30.0],            // (b) explicit list
    "interval":   10.0,                        // (c) every N seconds
    "scene":      0.4                           // (d) scene-change threshold, or "thumbnail"
  },
  "path":  "out/frame_%03d.png",              // templated: one integer token (or none for a single frame)
  "codec": "png",                              // image encoder: png (default) | mjpeg | webp
  "scale": "320:-2",                           // optional ffmpeg scale args
  "count": 25                                  // optional cap on frames emitted
}
```

Pulls one or more stills from a video to templated image files (afmpeg spec 0021) — the
bread-and-butter "poster at 5s / thumbnail strip / contact sheet" chore as typed fields rather
than an `fps`/`select` graph into an `image2` muxer. The seeking selectors (`timestamp` /
`timestamps` / `interval`) fast-seek to the keyframe at-or-before each target and decode forward
to the first frame ≥ target — cheap, not a full-stream decode; `scene` stream-decodes through
the `select='gt(scene,T)'` or `thumbnail` filter (which need the **intermediate** profile). Each
frame is optionally scaled, encoded, and written; the engine owns the naming and reports each:

```json
{"frames":[{"path":"out/frame_000.png","index":0,"timestamp":12.5}],"count":1}
```

`select` is a one-of (zero or multiple is rejected). `count` caps output — an uncapped `interval`
falls back to a built-in bound. The image codecs are native (png/mjpeg); `webp` rides spec 0018.

### `version` — report the vocabulary version

```jsonc
{ "op": "version" }
```

Prints the engine's highest supported vocabulary version + its FFmpeg build as JSON on
stdout — the machine-readable channel a consumer preflights before running jobs. Needs no
input, so it works before any media is mounted:

```json
{"vocab_version":8,"ffmpeg_version":"n8.1.2"}
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
| 3 | Seeking & time ranges (afmpeg spec 0014): `inputs[].seek {start, mode}`, `outputs[].duration` \| `end` (mutually exclusive), `outputs[].copy_ts`. Probe replies gain `start_sec`. |
| 4 | Input options & formats (afmpeg spec 0024): `inputs[].format` (forced demuxer), `inputs[].options` (demuxer dict, incl. raw geometry), and `N:v:K` indexed graph-input stream selection. |
| 5 | Container coverage (afmpeg spec 0015): `outputs[].format` (forced muxer), `outputs[].format_options` (muxer dict — segmenting/fragmentation); a `segmented` result marker. The container (de)muxer batch itself is a build-profile matter (intermediate), not a vocabulary one. |
| 6 | Frame extraction (afmpeg spec 0021): the new `op:"frames"` — pull stills by `select {timestamp \| timestamps \| interval \| scene}` to a templated `path`, with optional `codec`/`scale`/`count`. |
| 7 | Metadata & chapters (afmpeg spec 0020): `outputs[].metadata` (container tags), `outputs[].chapters` (`"copy"`/index passthrough), `outputs[].stream_metadata` (per-map `language`/`disposition`/`tags`). Probe replies gain container `tags`/`chapters` and per-stream `tags`/`disposition`/`language` (additive). |
| 8 | Subtitle streams (afmpeg spec 0019): `outputs[].subtitle_codec` (an encoder name or `"copy"`) + `N:s` subtitle map specifiers — extract/convert/copy subtitle tracks (the subtitle transcode lane). |

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
