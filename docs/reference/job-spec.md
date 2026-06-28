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
    **`probe` is implemented and runs today.** `process` is the next increment. The shapes
    below follow afmpeg [spec 0007 §4](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0007-libav-direct-engine.md);
    the `process` fields may still settle during implementation.

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
| `inputs[]` | Each input's path (resolved against the mounted filesystem) + demuxer options. |
| `filter` | An ffmpeg **filtergraph string**, parsed by libav (`avfilter_graph_parse2`). |
| `outputs[]` | Each output's path, the graph pads/streams to `map`, codecs, and muxer/encoder options. |

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

The vocabulary carries a version. A consumer (afmpeg) records the engine artifact version +
vocabulary version it was built against; a mismatch is caught rather than silently
misbehaving.
