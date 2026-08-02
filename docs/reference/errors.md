---
title: Errors & exit codes
description: Every message the engine writes to stderr, what caused it, and what to change — plus how a host should decide a job failed.
date: 2026-08-02
tags: [reference, errors]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Errors & exit codes

The engine reports a failure as **a single line on stderr and a non-zero exit code**. Nothing is
written to stdout on a failed job, so a host that sees an empty stdout has its answer on stderr.
Every message is prefixed `ffmpeg-wasi:` and, for the three job ops, by the op name.

## How to tell a job failed

| Exit code | Meaning | Act on it by |
|---:|---|---|
| `0` | success — stdout carries the result JSON | reading stdout |
| `1` | **a processing failure** during `process` or `frames` — an input would not open, an encoder would not start, a filtergraph would not configure | reading stderr; the job is not retryable unchanged |
| `2` | **a malformed request** — the spec is not valid JSON, names an unknown `op`, or has the wrong shape | fixing the spec |
| `3` | **the vocabulary is too new** — the spec's `version` exceeds this engine's | upgrading the engine, not the spec |

Exit `3` is deliberately distinct from `2` so a caller can tell *"upgrade ffmpeg-wasi"* from
*"fix the job"* without parsing the message. See
[version negotiation](driver-invocation-abi.md#version-negotiation).

!!! warning "Two `process` validation failures currently exit `0`"
    A `process` job whose **output entry has no `path`, or no video/audio/subtitle codec**, and one
    that sets **both `duration` and `end`**, print their error to stderr and then exit **`0`** with
    empty stdout — the malformed-request code never reaches the caller. A host that keys only on
    the exit code will read this as a success that produced no files.

    Until that is fixed, treat **stdout being empty on a `process` job as a failure** regardless of
    the exit code. Every other failure path exits `1`, `2` or `3` correctly.

## Dispatch and version-gate errors

These come from `src/driver.c`, before any op runs.

| Message | Exit | Cause | Fix |
|---|---:|---|---|
| `invalid job spec JSON` | `2` | `argv[1]` did not parse as JSON | the spec is passed as **one** argument — quote it as a single shell word |
| `unknown op <name>` | `2` | `"op"` is absent or not one of `probe`/`process`/`frames`/`version` | use one of the four [operations](job-spec.md#operations) |
| `job spec vocabulary version N newer than this engine supports (M); upgrade ffmpeg-wasi` | `3` | the spec's `version` field exceeds the engine's `AFMPEG_VOCAB_VERSION` | load a newer module, or emit a lower vocabulary version |

The version gate runs **before** dispatch, on every spec including `{"op":"version"}`. Do not stamp
a `version` field onto a `version` query — an engine older than your vocabulary would reject the one
query designed to detect exactly that.

## `probe` errors

| Message | Exit | Cause |
|---|---:|---|
| `probe: "inputs" must be an array` | `2` | `inputs` is missing or not an array |

`probe` does not fail on an unopenable input. It reports that input as
`{"path":"…","error":"could not open input"}` inside an otherwise normal reply and exits `0`, so one
bad file in a batch does not lose the results for the others.

## `process` errors — the spec is wrong

| Message | Exit | Fix |
|---|---:|---|
| `process: need at least one input and one output` | `2` | supply non-empty `inputs[]` and `outputs[]` |
| `process: each output needs path and a video, audio and/or subtitle codec` | `0` — see the warning above | give the output a `path` and at least one of `video_codec` / `audio_codec` / `subtitle_codec` |
| ``process: `duration` and `end` are mutually exclusive on <path>`` | `0` — see the warning above | set one or the other, never both |
| ``process: with multiple outputs each must set `map` `` | `1` | with two or more outputs, every one must claim its pads or streams |
| `process: too many outputs` | `1` | at most 8 — see [limits](limits.md#how-many-inputs-outputs-and-streams-can-one-job-have) |
| `process: too many graph inputs (max 32)` | `1` | fewer graph input pads |
| `process: too many copied streams` | `1` | at most 32 stream-copies per job |
| `process: too many subtitle streams` | `1` | at most 16 subtitle streams per job |
| `process: cannot parse map entry <s>` | `1` | a `map` entry must be a graph pad (`"[v]"`) or a stream specifier (`"0:v"`, `"0:a:0"`, `"0:0"`) |
| `process: map <s> references input N, only M given` | `1` | the map's input index is beyond `inputs[]` |
| `process: map <s> selects no stream` | `1` | the input has no stream matching that specifier |
| `process: cannot map graph input pad <s> (expected N:v / N:a / N:v:K)` | `1` | a filtergraph input label must be `[0:v]`, `[1:a]` or the indexed `[0:v:1]` |
| `process: filter references input N, only M given` | `1` | the graph names an input the spec does not supply |
| `process: input N has no <type> stream K` | `1` | the indexed stream selection points past the input's streams |
| `process: graph output pad [<p>] is not mapped to any output` | `1` | every graph output pad must appear in some output's `map` |
| `process: pad [<p>] is <type> but output <path> gives no codec for it` | `1` | an output receiving a video pad needs `video_codec`; an audio pad needs `audio_codec` |
| ``process: input N seek needs a non-negative `start` `` | `1` | `seek.start` must be ≥ 0 |
| `process: input N seek mode <m>` (the message goes on to name the two it wants) | `1` | `seek.mode` is `"fast"` or `"accurate"` |
| `process: accurate seek cannot apply to copied stream <s> (copy cuts on keyframes; use mode "fast")` | `1` | a stream-copy cannot be cut mid-GOP — re-encode, or use `"fast"` |

## `process` errors — the build cannot do it

These mean the spec is well-formed but names something the loaded build does not carry. The usual
cause is running a `lean` module where an `intermediate` or `full` one is needed —
check [codecs](codecs.md), [filters](filters.md) and
[containers](containers.md) for which profile carries what.

| Message | Exit | Cause |
|---|---:|---|
| `process: unknown encoder <name>` | `1` | no encoder of that name in this build — including a decode-only codec such as `prores`, and `libx264` outside the gpl variant |
| `process: unknown subtitle encoder <name>` | `1` | subtitle encoders need the **intermediate** profile |
| `process: no decoder for input N stream K` | `1` | the input's codec is not enabled in this build |
| `process: no decoder for subtitle stream` | `1` | the subtitle codec is not enabled |
| `process: unknown input format <name>` | `1` | `inputs[].format` names a demuxer this build does not carry |
| `process: cannot resolve output format for <path>` | `1` | the muxer could not be guessed from the extension, or `outputs[].format` names one that is absent |
| `process: bad filtergraph <s>` | `1` | libavfilter rejected the graph — most often a filter name that is not enabled |
| `process: bad bitstream filter <s>` | `1` | `bitstream_filters` names a BSF this build does not carry |
| `process: concat demuxer not built` | `1` | the concat demuxer is missing (it is in every profile, so this indicates a non-standard build) |

## `process` errors — I/O and runtime

| Message | Exit | Cause |
|---|---:|---|
| `process: cannot open input <path>` | `1` | the path is not present on the mounted filesystem, or the format could not be probed |
| `process: cannot open output <path>` | `1` | the output directory does not exist on the mounted filesystem, or is not writable |
| `process: cannot create concat list` | `1` | `/tmp` is not writable on the mounted filesystem — [see the concat note](limits.md#why-does-a-concat-input-fail-with-cannot-create-concat-list) |
| `process: cannot open concat playlist` | `1` | a segment named in `inputs[].concat` could not be opened |
| `process: cannot seek input N to <t>s` | `1` | the demuxer could not seek — some formats are not seekable |
| `process: input N: unknown demuxer option <key>` | `1` | a key in `inputs[].options` was not consumed by the demuxer; an unused key is an error, not a warning |
| `process: open encoder <name> failed` | `1` | the encoder rejected its `options` or the negotiated frame format |
| `process: write header for <path> failed` | `1` | the muxer rejected the stream set or its `format_options` |
| `process: filtergraph config failed` | `1` | the graph parsed but could not be configured — usually a format negotiation the enabled filters cannot satisfy |
| `process: <libav message>` | `1` | any other libav failure, rendered through `av_strerror` |

## `frames` errors

| Message | Exit | Fix |
|---|---:|---|
| `frames: need exactly one input` | `2` | `frames` takes exactly one input, never zero or several |
| ``frames: `select` object required`` | `2` | supply a `select` object |
| ``frames: `select` must set exactly one of timestamp/timestamps/interval/scene`` | `1` | the selector is a one-of; zero or two is rejected |
| ``frames: `path` template required`` | `2` | supply the output `path` |
| ``frames: `path` must contain zero or one integer template token (e.g. %03d)`` | `2` | one `%d`-style token at most; `%s` and `%n` are refused outright |
| ``frames: `path` needs an integer token (e.g. %03d) for multiple frames`` | `1` | a literal path can only name one file |
| `frames: unknown or non-image codec <name>` | `2` | `codec` must be a video encoder this build carries — `png` (default), `mjpeg`, or `webp` on the intermediate profile |
| `frames: interval must be > 0` | `1` | a zero or negative interval selects nothing |
| `frames: timestamps must be numbers` | `1` | every entry in `timestamps` is a number of seconds |
| `frames: scene must be a number (threshold) or "thumbnail"` | `1` | `scene` is a 0–1 threshold, or the literal `"thumbnail"` |
| `frames: no video stream in input` | `1` | `frames` needs a video stream; audio-only inputs are rejected |
| `frames: no decoder for input video` | `1` | the input's video codec is not enabled in this build |
| `frames: bad filter chain "<s>"` | `1` | the `scale` value is spliced into a filtergraph — check its syntax (`"320:-2"`) |
| `frames: cannot write <path>` | `1` | the output directory is absent or not writable on the mounted filesystem |
| `frames: unknown input format <name>` | `1` | `inputs[].format` names a demuxer this build does not carry |

The `scene` selector uses the `select` and `thumbnail` filters, which are **intermediate**-profile
only; on a `lean` build it fails as `frames: bad filter chain`.

## Warnings that do not fail a job

| Message | Behaviour |
|---|---|
| `unknown disposition flag <name> (ignored)` | An unrecognised entry in `stream_metadata.disposition` is skipped and the job continues. Check it against libav's disposition names (`default`, `forced`, `hearing_impaired`, …). |

## Related

- [Driver invocation & ABI](driver-invocation-abi.md) — where stdout, stderr and the exit code sit
  in the host contract.
- [Limits & what is not supported](limits.md) — the caps several of these messages report.
- [The job-spec vocabulary](job-spec.md) — the fields being validated.
