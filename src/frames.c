// ffmpeg-wasi — the "frames" operation: pull still frames out of a video into
// templated image files, driving libav directly over the mounted WASI fs.
//
// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Matt Cockayne
//
// A third first-class op beside probe/process (spec 0021). Frame extraction is a
// seek-and-grab-one access pattern, not process's stream-everything pass, so it
// is its own loop: for the seeking selectors (a single timestamp, an explicit
// list, or a regular interval) it fast-seeks to the keyframe at-or-before each
// target and decodes forward to the first frame ≥ target (cheap — 0014's seek,
// not a full decode); for `scene` it stream-decodes through the select/thumbnail
// filter. Each surviving frame is optionally scaled, encoded to an image codec
// (png/mjpeg/webp/…), and written to a printf-templated path the engine owns
// (frame_%03d.png). It shares process.c's conventions (buffersink pix_fmt
// negotiation, encoder-from-sink) but not its multi-output Ctx.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavutil/avutil.h>
#include <libavutil/opt.h>
#include <libavutil/mem.h>
#include <libavutil/error.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#include "frames.h"
#include "nativeio.h"

// A runaway interval over a long input needs a bound; `count` caps output but is
// optional, so an uncapped interval falls back to this (spec 0021 §6 open q).
#define FRAMES_DEFAULT_CAP 1000
#define FRAMES_MAX_TARGETS 4096

enum sel_kind { SEL_NONE, SEL_TIMESTAMP, SEL_TIMESTAMPS, SEL_INTERVAL, SEL_SCENE };

// The whole frames job, parsed and owned in one place for a single cleanup path.
typedef struct {
    AVFormatContext *fmt;
    int vstream;
    AVCodecContext *dec;
    AVFilterGraph *graph;
    AVFilterContext *src;   // buffersrc
    AVFilterContext *sink;  // buffersink
    AVCodecContext *enc;    // opened lazily from the negotiated sink format
    cJSON *result;          // {"frames":[…]}
    cJSON *jframes;
    int written;            // frames emitted so far
    int64_t pts_counter;    // synthetic monotonic pts fed to buffersrc
} Frames;

// ---- templated output naming --------------------------------------------

// template_int_conversions counts printf integer conversions in a path template
// and rejects any other conversion (%s/%n/… would be a format-string hazard on a
// caller-supplied string). Returns the count, or -1 if a non-integer conversion
// is present. "%%" is a literal percent, not a conversion.
static int template_int_conversions(const char *t) {
    int n = 0;
    for (const char *p = t; *p; p++) {
        if (*p != '%') continue;
        p++;
        if (*p == '%') continue;              // %% literal
        while (*p && strchr("-+ 0#", *p)) p++; // flags
        while (*p >= '0' && *p <= '9') p++;    // width
        if (*p == '.') { p++; while (*p >= '0' && *p <= '9') p++; } // precision
        if (*p == 'd' || *p == 'i' || *p == 'u' || *p == 'x' || *p == 'X' || *p == 'o') {
            n++;
        } else {
            return -1; // e.g. %s / %n — refuse
        }
    }
    return n;
}

// expand_path renders the output path for frame `index`. With one integer token
// it is snprintf(template, index); with none the template is literal (only valid
// for a single frame — the caller enforces that). ntok is the validated count.
static void expand_path(const char *tmpl, int ntok, int index, char *out, size_t n) {
    if (ntok == 1) {
        snprintf(out, n, tmpl, index); // validated to hold exactly one %d-style token
    } else {
        snprintf(out, n, "%s", tmpl);
    }
}

// ---- input / decoder ----------------------------------------------------

// open_video opens the single input (path + optional forced `format`/`options`,
// spec 0024-style; no concat here) and its best video stream's decoder.
static int open_video(Frames *f, const cJSON *in) {
    const char *path = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(in, "path"));

    const AVInputFormat *ifmt = NULL;
    const char *fmt_name = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(in, "format"));
    if (fmt_name && !(ifmt = av_find_input_format(fmt_name))) {
        fprintf(stderr, "ffmpeg-wasi: frames: unknown input format %s\n", fmt_name);
        return AVERROR_DEMUXER_NOT_FOUND;
    }

    AVDictionary *opts = NULL;
    const cJSON *od = cJSON_GetObjectItemCaseSensitive(in, "options"), *kv = NULL;
    if (cJSON_IsObject(od)) {
        cJSON_ArrayForEach(kv, od) {
            if (cJSON_IsString(kv)) av_dict_set(&opts, kv->string, kv->valuestring, 0);
        }
    }
    int rc = afio_open_input(&f->fmt, path, ifmt, &opts);
    av_dict_free(&opts);
    if (rc < 0) {
        fprintf(stderr, "ffmpeg-wasi: frames: cannot open input %s\n", path ? path : "(null)");
        return rc;
    }
    if ((rc = avformat_find_stream_info(f->fmt, NULL)) < 0) return rc;

    f->vstream = av_find_best_stream(f->fmt, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (f->vstream < 0) {
        fprintf(stderr, "ffmpeg-wasi: frames: no video stream in input\n");
        return f->vstream;
    }
    AVStream *st = f->fmt->streams[f->vstream];
    const AVCodec *dec = avcodec_find_decoder(st->codecpar->codec_id);
    if (!dec) { fprintf(stderr, "ffmpeg-wasi: frames: no decoder for input video\n"); return AVERROR_DECODER_NOT_FOUND; }
    f->dec = avcodec_alloc_context3(dec);
    if (!f->dec) return AVERROR(ENOMEM);
    if ((rc = avcodec_parameters_to_context(f->dec, st->codecpar)) < 0) return rc;
    f->dec->pkt_timebase = st->time_base;
    return avcodec_open2(f->dec, dec, NULL);
}

// ---- filter graph -------------------------------------------------------

// build_graph wires buffersrc → [chain] → buffersink, where the chain carries the
// selector filter (select/thumbnail) and an optional user scale. The sink's
// pix_fmts is pinned to the encoder's first format so the graph auto-inserts the
// conversion the image encoder needs (mirrors process.c's add_buffersink).
static int build_graph(Frames *f, const AVCodec *enc, const char *chain) {
    f->graph = avfilter_graph_alloc();
    if (!f->graph) return AVERROR(ENOMEM);

    AVStream *st = f->fmt->streams[f->vstream];
    AVRational tb = st->time_base;
    AVRational sar = f->dec->sample_aspect_ratio;
    char args[256];
    snprintf(args, sizeof(args),
             "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
             f->dec->width, f->dec->height, f->dec->pix_fmt,
             tb.num, tb.den, sar.num ? sar.num : 1, sar.den ? sar.den : 1);

    int rc = avfilter_graph_create_filter(&f->src, avfilter_get_by_name("buffer"),
                                          "in", args, NULL, f->graph);
    if (rc < 0) { fprintf(stderr, "ffmpeg-wasi: frames: buffersrc init failed\n"); return rc; }

    // buffersink: pix_fmts is an init-time option, so alloc → set → init (not
    // create_filter, which initialises before we can pin the encoder's format).
    f->sink = avfilter_graph_alloc_filter(f->graph, avfilter_get_by_name("buffersink"), "out");
    if (!f->sink) { fprintf(stderr, "ffmpeg-wasi: frames: buffersink alloc failed\n"); return AVERROR(ENOMEM); }
    // The supported list comes from avcodec_get_supported_config, not the AVCodec
    // fields it replaced (deprecated in FFmpeg 8, removed in 9.0). A NULL list means
    // the encoder accepts everything, so pin nothing — see process.c add_buffersink.
    const enum AVPixelFormat *pix_fmts = NULL;
    if ((rc = avcodec_get_supported_config(NULL, enc, AV_CODEC_CONFIG_PIX_FORMAT, 0,
                                           (const void **)&pix_fmts, NULL)) < 0)
        return rc;
    if (pix_fmts) {
        // FFmpeg 8's counted-array option (the deprecated "pix_fmts" binary option
        // crashes native format negotiation — see process.c add_buffersink).
        enum AVPixelFormat pf = pix_fmts[0];
        if ((rc = av_opt_set_array(f->sink, "pixel_formats", AV_OPT_SEARCH_CHILDREN,
                                   0, 1, AV_OPT_TYPE_PIXEL_FMT, &pf)) < 0)
            return rc;
    }
    if ((rc = avfilter_init_str(f->sink, NULL)) < 0) {
        fprintf(stderr, "ffmpeg-wasi: frames: buffersink init failed\n"); return rc;
    }

    AVFilterInOut *outputs = avfilter_inout_alloc();
    AVFilterInOut *inputs = avfilter_inout_alloc();
    if (!outputs || !inputs) { avfilter_inout_free(&outputs); avfilter_inout_free(&inputs); return AVERROR(ENOMEM); }
    outputs->name = av_strdup("in");  outputs->filter_ctx = f->src;  outputs->pad_idx = 0; outputs->next = NULL;
    inputs->name  = av_strdup("out"); inputs->filter_ctx  = f->sink; inputs->pad_idx  = 0; inputs->next  = NULL;

    rc = avfilter_graph_parse_ptr(f->graph, chain, &inputs, &outputs, NULL);
    avfilter_inout_free(&outputs);
    avfilter_inout_free(&inputs);
    if (rc < 0) { fprintf(stderr, "ffmpeg-wasi: frames: bad filter chain \"%s\"\n", chain); return rc; }

    return avfilter_graph_config(f->graph, NULL);
}

// ---- encode + write -----------------------------------------------------

// open_encoder_from_sink opens the image encoder from the graph's negotiated
// output format (the sink knows the final w/h/pix_fmt after config). Called once,
// lazily, before the first frame is written.
static int open_encoder_from_sink(Frames *f, const AVCodec *enc) {
    f->enc = avcodec_alloc_context3(enc);
    if (!f->enc) return AVERROR(ENOMEM);
    f->enc->width = av_buffersink_get_w(f->sink);
    f->enc->height = av_buffersink_get_h(f->sink);
    f->enc->pix_fmt = av_buffersink_get_format(f->sink);
    f->enc->sample_aspect_ratio = av_buffersink_get_sample_aspect_ratio(f->sink);
    f->enc->time_base = (AVRational){1, 25}; // intra images: nominal, unused downstream
    int rc = avcodec_open2(f->enc, enc, NULL);
    if (rc < 0) fprintf(stderr, "ffmpeg-wasi: frames: open image encoder failed\n");
    return rc;
}

// write_frame encodes one (already scaled/converted) frame and writes the single
// resulting packet — for an intra image codec that packet is the whole file — to
// `path`, then records it in the result JSON with its source timestamp `t_sec`.
static int write_frame(Frames *f, AVFrame *frame, const char *path, double t_sec) {
    AVPacket *pkt = av_packet_alloc();
    if (!pkt) return AVERROR(ENOMEM);
    int rc = avcodec_send_frame(f->enc, frame);
    if (rc < 0) goto done;
    rc = avcodec_receive_packet(f->enc, pkt);
    if (rc < 0) { // an intra image encoder yields its packet immediately
        fprintf(stderr, "ffmpeg-wasi: frames: encoder produced no packet for %s\n", path);
        goto done;
    }
    if ((rc = afio_write_file(path, pkt->data, (size_t)pkt->size)) < 0) {
        fprintf(stderr, "ffmpeg-wasi: frames: cannot write %s\n", path); goto done;
    }

    cJSON *jf = cJSON_CreateObject();
    cJSON_AddStringToObject(jf, "path", path);
    cJSON_AddNumberToObject(jf, "index", f->written);
    if (t_sec >= 0) cJSON_AddNumberToObject(jf, "timestamp", t_sec);
    cJSON_AddItemToArray(f->jframes, jf);
    f->written++;
    rc = 0;
done:
    av_packet_free(&pkt);
    return rc;
}

// emit_one pushes a decoded frame through the graph and writes whatever the sink
// yields (a scale/null chain is 1-in-1-out; select/thumbnail may emit 0 or 1).
// `t_sec` is the source time to record, `tmpl`/`ntok` the naming; capped by `cap`.
static int emit_one(Frames *f, AVFrame *decoded, double t_sec, const char *tmpl, int ntok, int cap) {
    decoded->pts = f->pts_counter++; // monotonic — seeks make source pts non-monotonic
    int rc = av_buffersrc_add_frame_flags(f->src, decoded, AV_BUFFERSRC_FLAG_KEEP_REF);
    if (rc < 0) return rc;

    AVFrame *out = av_frame_alloc();
    if (!out) return AVERROR(ENOMEM);
    while (f->written < cap) {
        rc = av_buffersink_get_frame(f->sink, out);
        if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) { rc = 0; break; }
        if (rc < 0) break;
        char path[1024];
        expand_path(tmpl, ntok, f->written, path, sizeof(path));
        rc = write_frame(f, out, path, t_sec);
        av_frame_unref(out);
        if (rc < 0) break;
    }
    av_frame_free(&out);
    return rc;
}

// ---- selectors ----------------------------------------------------------

// grab_at fast-seeks to the keyframe at-or-before `t_sec` and decodes forward to
// the first frame at-or-after it, then emits that one frame (spec 0021 §3 seek
// path). Missing targets (past EOF) are skipped, not an error.
static int grab_at(Frames *f, double t_sec, const char *tmpl, int ntok, int cap, AVPacket *pkt, AVFrame *frame) {
    AVStream *st = f->fmt->streams[f->vstream];
    int64_t ts = av_rescale_q((int64_t)(t_sec * AV_TIME_BASE), AV_TIME_BASE_Q, st->time_base);
    if (av_seek_frame(f->fmt, f->vstream, ts, AVSEEK_FLAG_BACKWARD) < 0) return 0; // unseekable → skip
    avcodec_flush_buffers(f->dec);

    int rc = 0;
    while ((rc = av_read_frame(f->fmt, pkt)) >= 0) {
        if (pkt->stream_index != f->vstream) { av_packet_unref(pkt); continue; }
        rc = avcodec_send_packet(f->dec, pkt);
        av_packet_unref(pkt);
        if (rc < 0) break;
        int done = 0;
        while ((rc = avcodec_receive_frame(f->dec, frame)) >= 0) {
            int64_t bt = frame->best_effort_timestamp != AV_NOPTS_VALUE ? frame->best_effort_timestamp : frame->pts;
            if (bt != AV_NOPTS_VALUE && bt < ts) { av_frame_unref(frame); continue; } // still before target
            double actual = bt != AV_NOPTS_VALUE ? bt * av_q2d(st->time_base) : t_sec;
            rc = emit_one(f, frame, actual, tmpl, ntok, cap);
            av_frame_unref(frame);
            done = 1;
            break;
        }
        if (done) return rc < 0 ? rc : 0;
        if (rc == AVERROR(EAGAIN)) continue;
        if (rc < 0) break;
    }

    // Only a genuine end of stream means "there is no frame at this timestamp".
    // Everything else -- a failed read from the host, a corrupt packet, a decoder
    // that gave up -- is a failure, and returning 0 for it reported a broken job
    // as a successful one with fewer frames than asked for (ffmpeg-wasi#20).
    return rc == AVERROR_EOF ? 0 : rc;
}

// grab_scene stream-decodes the whole input through the select/thumbnail chain,
// emitting each frame the filter passes (spec 0021 §3 scene path). The graph is
// flushed at EOF so thumbnail (which batches) yields its final pick.
static int grab_scene(Frames *f, const char *tmpl, int ntok, int cap, AVPacket *pkt, AVFrame *frame) {
    AVStream *st = f->fmt->streams[f->vstream];
    int rc = 0;
    while (f->written < cap) {
        if ((rc = av_read_frame(f->fmt, pkt)) < 0) break; // EOF (normal) or read error
        if (pkt->stream_index != f->vstream) { av_packet_unref(pkt); continue; }
        rc = avcodec_send_packet(f->dec, pkt);
        av_packet_unref(pkt);
        if (rc < 0) break;
        while ((rc = avcodec_receive_frame(f->dec, frame)) >= 0) {
            double t = frame->best_effort_timestamp != AV_NOPTS_VALUE ? frame->best_effort_timestamp * av_q2d(st->time_base) : -1;
            rc = emit_one(f, frame, t, tmpl, ntok, cap);
            av_frame_unref(frame);
            if (rc < 0 || f->written >= cap) break;
        }
        if (rc == AVERROR(EAGAIN)) rc = 0; // decoder wants more input — normal
        if (rc < 0) break;                 // a real decode/emit error
    }
    if (rc == AVERROR_EOF) rc = 0; // end of input is not an error

    // Flush the decoder, then the graph, so a batching filter (thumbnail) emits its
    // last pick. Propagate any write error instead of silently reporting success.
    if (rc >= 0) {
        avcodec_send_packet(f->dec, NULL);
        while (f->written < cap && (rc = avcodec_receive_frame(f->dec, frame)) >= 0) {
            double t = frame->best_effort_timestamp != AV_NOPTS_VALUE ? frame->best_effort_timestamp * av_q2d(st->time_base) : -1;
            rc = emit_one(f, frame, t, tmpl, ntok, cap);
            av_frame_unref(frame);
            if (rc < 0) break;
        }
        if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) rc = 0;
    }
    if (rc >= 0 && f->written < cap) {
        av_buffersrc_add_frame_flags(f->src, NULL, 0); // signal EOF to the graph
        AVFrame *out = av_frame_alloc();
        if (!out) return AVERROR(ENOMEM);
        while (f->written < cap && av_buffersink_get_frame(f->sink, out) >= 0) {
            char path[1024];
            expand_path(tmpl, ntok, f->written, path, sizeof(path));
            rc = write_frame(f, out, path, -1);
            av_frame_unref(out);
            if (rc < 0) break;
        }
        av_frame_free(&out);
    }
    return rc;
}

// ---- op -----------------------------------------------------------------

// parse_select validates the one-of selector and fills the target list (seek
// selectors) or the scene chain fragment. Returns the sel_kind, or SEL_NONE on a
// validation error (message already printed).
static enum sel_kind parse_select(const cJSON *sel, const AVFormatContext *fc, int cap,
                                  double *targets, int *n_targets, char *scene_frag, size_t frag_n) {
    const cJSON *single = cJSON_GetObjectItemCaseSensitive(sel, "timestamp");
    const cJSON *list = cJSON_GetObjectItemCaseSensitive(sel, "timestamps");
    const cJSON *interval = cJSON_GetObjectItemCaseSensitive(sel, "interval");
    const cJSON *scene = cJSON_GetObjectItemCaseSensitive(sel, "scene");

    int set = cJSON_IsNumber(single) + (cJSON_IsArray(list) && cJSON_GetArraySize(list) > 0)
            + cJSON_IsNumber(interval) + (cJSON_IsNumber(scene) || cJSON_IsString(scene));
    if (set != 1) {
        fprintf(stderr, "ffmpeg-wasi: frames: `select` must set exactly one of timestamp/timestamps/interval/scene\n");
        return SEL_NONE;
    }

    if (cJSON_IsNumber(single)) {
        targets[0] = single->valuedouble;
        *n_targets = 1;
        return SEL_TIMESTAMP;
    }
    if (cJSON_IsArray(list) && cJSON_GetArraySize(list) > 0) {
        int n = 0;
        const cJSON *e = NULL;
        cJSON_ArrayForEach(e, list) {
            if (!cJSON_IsNumber(e)) { fprintf(stderr, "ffmpeg-wasi: frames: timestamps must be numbers\n"); return SEL_NONE; }
            if (n >= cap || n >= FRAMES_MAX_TARGETS) break;
            targets[n++] = e->valuedouble;
        }
        *n_targets = n;
        return SEL_TIMESTAMPS;
    }
    if (cJSON_IsNumber(interval)) {
        double step = interval->valuedouble;
        if (step <= 0) { fprintf(stderr, "ffmpeg-wasi: frames: interval must be > 0\n"); return SEL_NONE; }
        double dur = -1;
        if (fc && fc->duration != AV_NOPTS_VALUE) dur = (double)fc->duration / AV_TIME_BASE;
        int n = 0;
        for (double t = 0; (dur < 0 || t < dur) && n < cap && n < FRAMES_MAX_TARGETS; t += step)
            targets[n++] = t;
        *n_targets = n;
        return SEL_INTERVAL;
    }
    // scene: a numeric threshold → select='gt(scene,T)'; the string "thumbnail" →
    // the thumbnail filter.
    if (cJSON_IsString(scene) && strcmp(scene->valuestring, "thumbnail") == 0) {
        snprintf(scene_frag, frag_n, "thumbnail");
    } else if (cJSON_IsNumber(scene)) {
        snprintf(scene_frag, frag_n, "select='gt(scene\\,%f)'", scene->valuedouble);
    } else {
        fprintf(stderr, "ffmpeg-wasi: frames: scene must be a number (threshold) or \"thumbnail\"\n");
        return SEL_NONE;
    }
    return SEL_SCENE;
}

int op_frames(const cJSON *spec) {
    const cJSON *inputs = cJSON_GetObjectItemCaseSensitive(spec, "inputs");
    if (!cJSON_IsArray(inputs) || cJSON_GetArraySize(inputs) != 1) {
        fprintf(stderr, "ffmpeg-wasi: frames: need exactly one input\n");
        return 2;
    }
    const cJSON *sel = cJSON_GetObjectItemCaseSensitive(spec, "select");
    if (!cJSON_IsObject(sel)) { fprintf(stderr, "ffmpeg-wasi: frames: `select` object required\n"); return 2; }

    const char *tmpl = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "path"));
    if (!tmpl) { fprintf(stderr, "ffmpeg-wasi: frames: `path` template required\n"); return 2; }
    int ntok = template_int_conversions(tmpl);
    if (ntok < 0 || ntok > 1) {
        fprintf(stderr, "ffmpeg-wasi: frames: `path` must contain zero or one integer template token (e.g. %%03d)\n");
        return 2;
    }

    const char *codec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "codec"));
    if (!codec) codec = "png";
    const AVCodec *enc = avcodec_find_encoder_by_name(codec);
    if (!enc || enc->type != AVMEDIA_TYPE_VIDEO) {
        fprintf(stderr, "ffmpeg-wasi: frames: unknown or non-image codec %s\n", codec);
        return 2;
    }
    const char *scale = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "scale"));

    const cJSON *jcount = cJSON_GetObjectItemCaseSensitive(spec, "count");
    int cap = cJSON_IsNumber(jcount) && jcount->valueint > 0 ? jcount->valueint : FRAMES_DEFAULT_CAP;

    Frames f = {0};
    f.result = cJSON_CreateObject();
    f.jframes = cJSON_AddArrayToObject(f.result, "frames");

    int rc = open_video(&f, cJSON_GetArrayItem(inputs, 0));
    if (rc < 0) goto end;

    // Resolve the selector (interval needs the probed duration). The target list
    // is heap-allocated — up to FRAMES_MAX_TARGETS doubles is too much for the
    // wasm stack.
    double *targets = av_calloc(FRAMES_MAX_TARGETS, sizeof(*targets));
    if (!targets) { rc = AVERROR(ENOMEM); goto end; }
    int n_targets = 0;
    char scene_frag[128] = {0};
    enum sel_kind kind = parse_select(sel, f.fmt, cap, targets, &n_targets, scene_frag, sizeof(scene_frag));
    if (kind == SEL_NONE) { av_freep(&targets); rc = AVERROR(EINVAL); goto end; }

    // A literal (token-less) path can only name one file.
    int expected = kind == SEL_SCENE ? cap : n_targets;
    if (ntok == 0 && expected > 1) {
        fprintf(stderr, "ffmpeg-wasi: frames: `path` needs an integer token (e.g. %%03d) for multiple frames\n");
        rc = AVERROR(EINVAL); goto end;
    }

    // Assemble the filter chain: selector fragment (scene only) + optional scale;
    // buffersink converts to the encoder's pixel format regardless.
    char chain[256];
    if (kind == SEL_SCENE && scale) snprintf(chain, sizeof(chain), "%s,scale=%s", scene_frag, scale);
    else if (kind == SEL_SCENE)     snprintf(chain, sizeof(chain), "%s", scene_frag);
    else if (scale)                 snprintf(chain, sizeof(chain), "scale=%s", scale);
    else                            snprintf(chain, sizeof(chain), "null");

    if ((rc = build_graph(&f, enc, chain)) < 0) goto end;
    if ((rc = open_encoder_from_sink(&f, enc)) < 0) goto end;

    AVPacket *pkt = av_packet_alloc();
    AVFrame *frame = av_frame_alloc();
    if (!pkt || !frame) { rc = AVERROR(ENOMEM); av_packet_free(&pkt); av_frame_free(&frame); goto end; }

    if (kind == SEL_SCENE) {
        rc = grab_scene(&f, tmpl, ntok, cap, pkt, frame);
    } else {
        for (int i = 0; i < n_targets && f.written < cap; i++) {
            if ((rc = grab_at(&f, targets[i], tmpl, ntok, cap, pkt, frame)) < 0) break;
        }
    }
    av_packet_free(&pkt);
    av_frame_free(&frame);
    av_freep(&targets);
    if (rc < 0) goto end;

    cJSON_AddNumberToObject(f.result, "count", f.written);
    char *json = cJSON_PrintUnformatted(f.result);
    if (json) { printf("%s\n", json); free(json); }

end:
    if (rc < 0) {
        char err[128];
        av_strerror(rc, err, sizeof(err));
        fprintf(stderr, "ffmpeg-wasi: frames: %s\n", err);
    }
    avcodec_free_context(&f.enc);
    avcodec_free_context(&f.dec);
    avfilter_graph_free(&f.graph);
    afio_close_input(&f.fmt);
    cJSON_Delete(f.result);
    return rc < 0 ? 1 : 0;
}
