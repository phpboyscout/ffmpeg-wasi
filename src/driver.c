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
// `--capabilities` prints the same ground truth as one line of JSON, listing
// every linked component by kind, for the conformance suite (spec 0036).
//
// Status: probe implemented; process is the next increment (spec 0007 §4).

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavutil/avutil.h>
#include <libavcodec/avcodec.h>
#include <libavcodec/bsf.h>       // av_bsf_iterate (--capabilities)
#include <libavformat/avformat.h>
#include <libavformat/avio.h>     // avio_enum_protocols (--capabilities)
#include <libavfilter/avfilter.h>

#include "third_party/cJSON/cJSON.h"
#include "process.h"
#include "frames.h"
#include "meta.h"
#include "nativeio.h"

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
//   8 — subtitle streams (spec 0019): outputs[].subtitle_codec (an encoder name
//       or "copy") + "N:s" subtitle-stream map specifiers — extract/convert/copy
//       subtitle tracks (the AVMEDIA_TYPE_SUBTITLE lane)
//   9 — progress side-channel (spec 0032 / afmpeg 0031 phase B): a top-level
//       "progress":true on an op:"process" job makes the engine emit NDJSON
//       {frame,out_time_us,total_size} records to /dev/afmpeg-progress as it
//       muxes. Purely additive and opt-in — absent/false behaves exactly as v8.
// A spec whose "version" exceeds this is rejected in main() rather than having
// its unknown fields silently dropped. Absent "version" == 0 (pre-gate).
#define AFMPEG_VOCAB_VERSION 9

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

// ---- machine-readable capability dump (spec 0036 D2) ---------------------
//
// `--capabilities` prints ONE LINE OF JSON on stdout naming every component
// actually linked into this binary, by kind — plus the engine identity, so one
// call answers both "which artifact is this" and "what is in it".
//
// Built by ITERATING what libav registered, never by probing a list of names.
// Probing can only confirm what the caller already suspected, so it can never
// surface a component gained or lost silently; iteration reports what is there.
// That is what lets the conformance check (spec 0036 D3) survive an FFmpeg bump.
//
// This is an INVOCATION MODE like --report, not a job op: it carries no
// AFMPEG_VOCAB_VERSION implication and needs no version stamp.

// has_str reports whether arr already contains s (the component lists are
// small; a linear scan is cheaper than carrying a set).
static int has_str(const cJSON *arr, const char *s) {
    const cJSON *it = NULL;
    cJSON_ArrayForEach(it, arr) {
        if (cJSON_IsString(it) && it->valuestring && strcmp(it->valuestring, s) == 0) return 1;
    }
    return 0;
}

// add_names appends a component name to arr, deduplicated. libav gives some
// formats a COMMA-SEPARATED ALIAS LIST as their name — the mov demuxer is
// "mov,mp4,m4a,3gp,3g2,mj2" — while build/libav.sh enables them one alias at a
// time (--enable-demuxer=mov). Splitting here means the consumer compares like
// with like instead of reimplementing this quirk.
static void add_names(cJSON *arr, const char *name) {
    if (!arr || !name) return;
    while (*name) {
        const char *comma = strchr(name, ',');
        size_t len = comma ? (size_t)(comma - name) : strlen(name);
        if (len > 0 && len < 128) {
            char buf[128];
            memcpy(buf, name, len);
            buf[len] = '\0';
            if (!has_str(arr, buf)) cJSON_AddItemToArray(arr, cJSON_CreateString(buf));
        }
        if (!comma) break;
        name = comma + 1;
    }
}

static int capabilities(void) {
    cJSON *out = cJSON_CreateObject();
    if (!out) return 1;

    cJSON_AddNumberToObject(out, "vocab_version", AFMPEG_VOCAB_VERSION);
    cJSON_AddStringToObject(out, "ffmpeg_version", av_version_info());

    cJSON *encoders  = cJSON_AddArrayToObject(out, "encoders");
    cJSON *decoders  = cJSON_AddArrayToObject(out, "decoders");
    cJSON *muxers    = cJSON_AddArrayToObject(out, "muxers");
    cJSON *demuxers  = cJSON_AddArrayToObject(out, "demuxers");
    cJSON *filters   = cJSON_AddArrayToObject(out, "filters");
    cJSON *bsfs      = cJSON_AddArrayToObject(out, "bsfs");
    cJSON *protocols = cJSON_AddArrayToObject(out, "protocols");
    cJSON *parsers   = cJSON_AddArrayToObject(out, "parsers");
    if (!encoders || !decoders || !muxers || !demuxers || !filters || !bsfs || !protocols || !parsers) {
        cJSON_Delete(out);
        return 1;
    }

    void *it = NULL;
    const AVCodec *codec = NULL;
    while ((codec = av_codec_iterate(&it))) {
        if (av_codec_is_encoder(codec)) add_names(encoders, codec->name);
        if (av_codec_is_decoder(codec)) add_names(decoders, codec->name);
    }

    it = NULL;
    const AVOutputFormat *ofmt = NULL;
    while ((ofmt = av_muxer_iterate(&it))) add_names(muxers, ofmt->name);

    it = NULL;
    const AVInputFormat *ifmt = NULL;
    while ((ifmt = av_demuxer_iterate(&it))) add_names(demuxers, ifmt->name);

    it = NULL;
    const AVFilter *filter = NULL;
    while ((filter = av_filter_iterate(&it))) add_names(filters, filter->name);

    it = NULL;
    const AVBitStreamFilter *bsf = NULL;
    while ((bsf = av_bsf_iterate(&it))) add_names(bsfs, bsf->name);

    // Parsers are the one kind libav does not name: AVCodecParser carries only
    // codec_ids. configure names them after the codec (--enable-parser=av1), so
    // resolve each id back through the codec descriptor to get the same spelling.
    it = NULL;
    const AVCodecParser *parser = NULL;
    while ((parser = av_parser_iterate(&it))) {
        // sizeof rather than a named bound: the array's width is an internal
        // detail that has changed across FFmpeg releases, and this suite exists
        // to survive exactly that kind of change.
        const size_t nids = sizeof(parser->codec_ids) / sizeof(parser->codec_ids[0]);
        for (size_t i = 0; i < nids && parser->codec_ids[i] != AV_CODEC_ID_NONE; i++) {
            const AVCodecDescriptor *d = avcodec_descriptor_get((enum AVCodecID)parser->codec_ids[i]);
            if (d) add_names(parsers, d->name);
        }
    }

    // Protocols are enumerated separately for input and output; build/libav.sh
    // does not distinguish (--enable-protocol=file,pipe), so report the union.
    for (int output = 0; output <= 1; output++) {
        void *opaque = NULL;
        const char *name = NULL;
        while ((name = avio_enum_protocols(&opaque, output))) add_names(protocols, name);
    }

    char *json = cJSON_PrintUnformatted(out);
    cJSON_Delete(out);
    if (!json) return 1;
    printf("%s\n", json);
    free(json);
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

// probe_input opens one input and appends its container/stream info. It honours a
// forced `format` (demuxer) and `options` (demuxer dict), so a raw/headerless
// input — openable only with those (spec 0024) — is probeable too.
// Returns 0, or non-zero for a MALFORMED REQUEST — distinct from an input that
// could not be opened, which stays a per-input "error" field and exit 0 so one
// bad file in a batch does not lose the others.
//
// probe used to be more forgiving than process about identical input, which makes
// it a poor instrument for deciding whether a job will run: a missing or
// non-string `path` became an I/O error, and an unknown `format` silently fell
// back to autodetection where process refuses it outright (ffmpeg-wasi#49).
static int probe_input(cJSON *out_inputs, const cJSON *in) {
    const cJSON *jp = cJSON_GetObjectItemCaseSensitive(in, "path");
    if (!cJSON_IsString(jp)) {
        fprintf(stderr, "ffmpeg-wasi: probe: each input needs a string `path`\n");
        return 2;
    }
    const char *path = jp->valuestring;
    cJSON *ji = cJSON_CreateObject();
    cJSON_AddStringToObject(ji, "path", path);

    const AVInputFormat *ifmt = NULL;
    const char *fmt_name = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(in, "format"));
    if (fmt_name) {
        ifmt = av_find_input_format(fmt_name);
        if (!ifmt) {
            // process rejects this; probe silently auto-probed instead, so the two
            // ops disagreed about the same spec.
            fprintf(stderr, "ffmpeg-wasi: probe: unknown input format %s\n", fmt_name);
            cJSON_Delete(ji);
            return 2;
        }
    }
    AVDictionary *opts = NULL;
    const cJSON *od = cJSON_GetObjectItemCaseSensitive(in, "options"), *kv = NULL;
    if (cJSON_IsObject(od)) {
        cJSON_ArrayForEach(kv, od) {
            if (cJSON_IsString(kv)) av_dict_set(&opts, kv->string, kv->valuestring, 0);
        }
    }

    AVFormatContext *fmt = NULL;
    int rc = afio_open_input(&fmt, path, ifmt, &opts);
    av_dict_free(&opts);
    if (rc < 0) {
        cJSON_AddStringToObject(ji, "error", "could not open input");
        cJSON_AddItemToArray(out_inputs, ji);
        return 0;
    }
    // Discarding this let probe report incomplete or wrong stream, duration and
    // chapter information with nothing to distinguish it from a good read. It is
    // reported the same way an unopenable input is — per input, not fatal.
    if (avformat_find_stream_info(fmt, NULL) < 0) {
        cJSON_AddStringToObject(ji, "error", "could not read stream information");
        cJSON_AddItemToArray(out_inputs, ji);
        afio_close_input(&fmt);
        return 0;
    }

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

    afio_close_input(&fmt);
    cJSON_AddItemToArray(out_inputs, ji);
    return 0;
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
        int rc = probe_input(out_inputs, in);
        if (rc != 0) { cJSON_Delete(out); return rc; }
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
    if (strcmp(argv[1], "--capabilities") == 0) {
        return capabilities();
    }

    // ParseWithOpts with require_null_terminated, so a valid object followed by
    // trailing text is refused. cJSON_Parse accepts it, and the ABI says the
    // driver takes ONE argument which is a JSON job spec — quietly running
    // something that is not one is the same class as #23, and it makes a buggy
    // host's malformed request succeed, which is a poor way to discover the host
    // is buggy (ffmpeg-wasi#50).
    cJSON *spec = cJSON_ParseWithOpts(argv[1], NULL, 1);
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
