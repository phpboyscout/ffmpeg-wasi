// ffmpeg-wasi — the libav-direct engine.
//
// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Matt Cockayne
//
// OUR engine: a small WASI program that links FFmpeg's libav* libraries and
// drives them directly — no ffmpeg CLI, no threads. The source is MIT; the
// linked *artifact* inherits libav*'s licence (LGPL by default, GPL with
// libx264). See the repository README for the licensing model.
//
// It reads a structured JSON job spec as argv[1] and dispatches on "op":
//   {"op":"probe","inputs":[{"path":"in/clip.mp4"}]}   → stream info as JSON
//   {"op":"process", ...}                              → transcode/filter/mux
//   {"op":"version"}                                   → engine vocab version JSON
// Paths resolve against the mounted WASI filesystem. Results go to stdout;
// errors to stderr with a non-zero exit. See the reference docs (job-spec.md).
//
// `--report` (or no args) prints a capability report — a build smoke test.
//
// Status: probe implemented; process is the next increment (spec 0007 §4).

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavutil/avutil.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>

#include "third_party/cJSON/cJSON.h"
#include "process.h"
#include "frames.h"
#include "meta.h"

// AFMPEG_VOCAB_VERSION is the highest job-spec vocabulary version this engine
// understands (spec 0007 §4 contract; afmpeg roadmap Phase 1 version-gating).
// It increments additively, once per landed vocabulary spec, in merge order:
//   1 — baseline + the version gate (op:version); no process/probe field changes
//   2 — stream copy / bitstream filters (spec 0013): the "copy" codec sentinel,
//       "in:type[:idx]" map specifiers, and outputs[].bitstream_filters
//   3 — seeking & time ranges (spec 0014): inputs[].seek {start, mode},
//       outputs[].duration | end (mutually exclusive), outputs[].copy_ts
//   4 — input options & formats (spec 0024): inputs[].format (forced demuxer),
//       inputs[].options (demuxer dict, incl. raw geometry), and N:v:K indexed
//       graph-input stream selection
//   5 — container coverage (spec 0015): outputs[].format (forced muxer),
//       outputs[].format_options (muxer dict — segmenting/fragmentation);
//       the container (de)muxer batch is a build-profile matter, not a vocab one
//   6 — frame extraction (spec 0021): the new op:"frames" — pull stills by
//       select {timestamp | timestamps | interval | scene} to a templated
//       path, optional codec/scale/count
//   7 — metadata & chapters (spec 0020): outputs[].metadata (container tags),
//       outputs[].chapters ("copy"/input index), outputs[].stream_metadata
//       (per-map tags/language/disposition). Probe replies gain tags/chapters
//       and per-stream tags/disposition/language (additive read side).
// A spec whose "version" exceeds this is rejected in main() rather than having
// its unknown fields silently dropped. Absent "version" == 0 (pre-gate).
#define AFMPEG_VOCAB_VERSION 7

// EXIT_VERSION_TOO_NEW signals a job spec newer than this engine supports —
// distinct from a malformed spec (2) so a caller can tell "upgrade the engine"
// from "fix the spec".
#define EXIT_VERSION_TOO_NEW 3

// ---- capability report (build smoke test) -------------------------------

static int report(void) {
    printf("ffmpeg-wasi engine\n");
    printf("vocab_version: %d\n", AFMPEG_VOCAB_VERSION);
    printf("ffmpeg: %s\n", av_version_info());
    printf("libavcodec %u  libavformat %u  libavfilter %u\n",
           avcodec_version(), avformat_version(), avfilter_version());

    printf("encoders:\n");
    const char *enc[] = {"libopenh264", "libx264", "mjpeg", "aac", "flac", "pcm_s16le", NULL};
    for (int i = 0; enc[i]; i++) {
        printf("  %-11s %s\n", enc[i], avcodec_find_encoder_by_name(enc[i]) ? "yes" : "no");
    }

    printf("decoders:\n");
    const char *dec[] = {"h264", "hevc", "vp9", "aac", "mp3", "opus", "flac", NULL};
    for (int i = 0; dec[i]; i++) {
        printf("  %-10s %s\n", dec[i], avcodec_find_decoder_by_name(dec[i]) ? "yes" : "no");
    }
    return 0;
}

// ---- op: probe ----------------------------------------------------------

// describe_stream appends one stream's details to the streams array.
static void describe_stream(cJSON *streams, unsigned index, const AVStream *st) {
    const AVCodecParameters *cp = st->codecpar;
    cJSON *js = cJSON_CreateObject();
    cJSON_AddNumberToObject(js, "index", index);

    const char *type = av_get_media_type_string(cp->codec_type);
    cJSON_AddStringToObject(js, "type", type ? type : "unknown");

    const char *codec = avcodec_get_name(cp->codec_id);
    cJSON_AddStringToObject(js, "codec", codec ? codec : "unknown");

    if (cp->codec_type == AVMEDIA_TYPE_VIDEO) {
        cJSON_AddNumberToObject(js, "width", cp->width);
        cJSON_AddNumberToObject(js, "height", cp->height);
    } else if (cp->codec_type == AVMEDIA_TYPE_AUDIO) {
        cJSON_AddNumberToObject(js, "sample_rate", cp->sample_rate);
        cJSON_AddNumberToObject(js, "channels", cp->ch_layout.nb_channels);
    }

    // Metadata (spec 0020, additive): the stream's tags, its decoded disposition
    // flags, and its language (hoisted from the tags for convenience).
    const AVDictionaryEntry *lang = av_dict_get(st->metadata, "language", NULL, 0);
    if (lang) cJSON_AddStringToObject(js, "language", lang->value);
    meta_add_disposition(js, st->disposition);
    meta_add_tags(js, st->metadata);

    cJSON_AddItemToArray(streams, js);
}

// probe_input opens one input and appends its container/stream info.
static void probe_input(cJSON *out_inputs, const char *path) {
    cJSON *ji = cJSON_CreateObject();
    cJSON_AddStringToObject(ji, "path", path ? path : "");

    AVFormatContext *fmt = NULL;
    int rc = avformat_open_input(&fmt, path, NULL, NULL);
    if (rc < 0) {
        cJSON_AddStringToObject(ji, "error", "could not open input");
        cJSON_AddItemToArray(out_inputs, ji);
        return;
    }
    avformat_find_stream_info(fmt, NULL);

    if (fmt->iformat && fmt->iformat->name) {
        cJSON_AddStringToObject(ji, "format", fmt->iformat->name);
    }
    if (fmt->duration != AV_NOPTS_VALUE) {
        cJSON_AddNumberToObject(ji, "duration_sec", (double)fmt->duration / AV_TIME_BASE);
    }
    if (fmt->start_time != AV_NOPTS_VALUE) {
        cJSON_AddNumberToObject(ji, "start_sec", (double)fmt->start_time / AV_TIME_BASE);
    }
    // Container tags + chapters (spec 0020, additive). Chapter start/end are
    // rescaled from the chapter's own time_base to seconds.
    meta_add_tags(ji, fmt->metadata);
    if (fmt->nb_chapters > 0) {
        cJSON *chapters = cJSON_AddArrayToObject(ji, "chapters");
        for (unsigned i = 0; i < fmt->nb_chapters; i++) {
            const AVChapter *ch = fmt->chapters[i];
            cJSON *jc = cJSON_CreateObject();
            cJSON_AddNumberToObject(jc, "start", ch->start * av_q2d(ch->time_base));
            cJSON_AddNumberToObject(jc, "end", ch->end * av_q2d(ch->time_base));
            const AVDictionaryEntry *title = av_dict_get(ch->metadata, "title", NULL, 0);
            if (title) cJSON_AddStringToObject(jc, "title", title->value);
            cJSON_AddItemToArray(chapters, jc);
        }
    }

    cJSON *streams = cJSON_AddArrayToObject(ji, "streams");
    for (unsigned i = 0; i < fmt->nb_streams; i++) {
        describe_stream(streams, i, fmt->streams[i]);
    }

    avformat_close_input(&fmt);
    cJSON_AddItemToArray(out_inputs, ji);
}

static int op_probe(const cJSON *spec) {
    const cJSON *inputs = cJSON_GetObjectItemCaseSensitive(spec, "inputs");
    if (!cJSON_IsArray(inputs)) {
        fprintf(stderr, "ffmpeg-wasi: probe: \"inputs\" must be an array\n");
        return 2;
    }

    cJSON *out = cJSON_CreateObject();
    cJSON *out_inputs = cJSON_AddArrayToObject(out, "inputs");

    const cJSON *in = NULL;
    cJSON_ArrayForEach(in, inputs) {
        const cJSON *p = cJSON_GetObjectItemCaseSensitive(in, "path");
        probe_input(out_inputs, cJSON_GetStringValue(p));
    }

    char *json = cJSON_PrintUnformatted(out);
    if (json) {
        printf("%s\n", json);
        free(json);
    }
    cJSON_Delete(out);
    return 0;
}

// ---- dispatch -----------------------------------------------------------

// op_version reports the engine's job-spec vocabulary version as JSON on stdout,
// the machine-readable channel afmpeg preflights at New() to detect a module too
// old for the vocabulary it emits. Needs no input, so it works before any job.
static int op_version(void) {
    cJSON *out = cJSON_CreateObject();
    cJSON_AddNumberToObject(out, "vocab_version", AFMPEG_VOCAB_VERSION);
    cJSON_AddStringToObject(out, "ffmpeg_version", av_version_info());
    char *json = cJSON_PrintUnformatted(out);
    if (json) {
        printf("%s\n", json);
        free(json);
    }
    cJSON_Delete(out);
    return 0;
}

int main(int argc, char **argv) {
    if (argc < 2 || strcmp(argv[1], "--report") == 0) {
        return report();
    }

    cJSON *spec = cJSON_Parse(argv[1]);
    if (!spec) {
        fprintf(stderr, "ffmpeg-wasi: invalid job spec JSON\n");
        return 2;
    }

    // Version gate: reject a spec that declares a vocabulary newer than this
    // engine understands, rather than silently dropping its unknown fields
    // (afmpeg stamps "version"; absent == 0, the pre-gate baseline).
    const cJSON *ver = cJSON_GetObjectItemCaseSensitive(spec, "version");
    if (cJSON_IsNumber(ver) && ver->valueint > AFMPEG_VOCAB_VERSION) {
        fprintf(stderr, "ffmpeg-wasi: job spec vocabulary version %d newer than this engine supports (%d); upgrade ffmpeg-wasi\n",
                ver->valueint, AFMPEG_VOCAB_VERSION);
        cJSON_Delete(spec);
        return EXIT_VERSION_TOO_NEW;
    }

    const char *op = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "op"));
    int rc;
    if (op && strcmp(op, "probe") == 0) {
        rc = op_probe(spec);
    } else if (op && strcmp(op, "process") == 0) {
        rc = op_process(spec);
    } else if (op && strcmp(op, "frames") == 0) {
        rc = op_frames(spec);
    } else if (op && strcmp(op, "version") == 0) {
        rc = op_version();
    } else {
        fprintf(stderr, "ffmpeg-wasi: unknown op %s\n", op ? op : "(none)");
        rc = 2;
    }

    cJSON_Delete(spec);
    return rc;
}
