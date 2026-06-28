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

// ---- capability report (build smoke test) -------------------------------

static int report(void) {
    printf("ffmpeg-wasi engine\n");
    printf("ffmpeg: %s\n", av_version_info());
    printf("libavcodec %u  libavformat %u  libavfilter %u\n",
           avcodec_version(), avformat_version(), avfilter_version());
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

int main(int argc, char **argv) {
    if (argc < 2 || strcmp(argv[1], "--report") == 0) {
        return report();
    }

    cJSON *spec = cJSON_Parse(argv[1]);
    if (!spec) {
        fprintf(stderr, "ffmpeg-wasi: invalid job spec JSON\n");
        return 2;
    }

    const char *op = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "op"));
    int rc;
    if (op && strcmp(op, "probe") == 0) {
        rc = op_probe(spec);
    } else if (op && strcmp(op, "process") == 0) {
        fprintf(stderr, "ffmpeg-wasi: op \"process\" not yet implemented\n");
        rc = 3;
    } else {
        fprintf(stderr, "ffmpeg-wasi: unknown op %s\n", op ? op : "(none)");
        rc = 2;
    }

    cJSON_Delete(spec);
    return rc;
}
