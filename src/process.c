// ffmpeg-wasi — the "process" operation: transcode/filter/mux driving libav
// directly, over the mounted WASI filesystem.
//
// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Matt Cockayne
//
// v2: the full filter_complex. N inputs feed one filter graph (parsed by
// avfilter_graph_parse2) whose labelled outputs are each encoded and muxed into
// a single output file. The `filter` field is the complete ffmpeg filtergraph
// string with pad labels ([0:v], [1:a], …, [vout], [aout]); each output pad is
// encoded with the output's video_codec (video pads) or audio_codec (audio
// pads). With no `filter`, a passthrough graph is generated for input 0's
// streams. (Single output file; the `map` field is accepted but every graph
// output pad is muxed.)

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavutil/avutil.h>
#include <libavutil/avstring.h>
#include <libavutil/opt.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#include "process.h"

#define MAX_INPUTS 32
#define MAX_GIN 32
#define MAX_GOUT 8

// One graph input: a decoded input stream feeding a buffersrc.
typedef struct {
    int in_idx;
    int st_idx;
    AVCodecContext *dec;
    AVFilterContext *src;
} GIn;

// One graph output: a buffersink encoded into an output stream.
typedef struct {
    enum AVMediaType type;
    AVFilterContext *sink;
    AVCodecContext *enc;
    AVStream *ost;
} GOut;

typedef struct {
    AVFormatContext *in[MAX_INPUTS];
    int n_in;
    int eof[MAX_INPUTS];
    AVFilterGraph *graph;
    GIn gin[MAX_GIN];
    int n_gin;
    GOut gout[MAX_GOUT];
    int n_gout;
    AVFormatContext *ofmt;
    AVDictionary *enc_opts;
} Ctx;

// parse_input_pad reads a "N:v" / "N:a" graph input label into an input index +
// media type.
static int parse_input_pad(const char *name, int *in_idx, enum AVMediaType *type) {
    char t = 0;
    if (sscanf(name, "%d:%c", in_idx, &t) != 2) return -1;
    if (t == 'v' || t == 'V') *type = AVMEDIA_TYPE_VIDEO;
    else if (t == 'a' || t == 'A') *type = AVMEDIA_TYPE_AUDIO;
    else return -1;
    return 0;
}

// add_buffersrc opens the decoder for one input pad and wires a buffersrc into
// the graph at the pad it feeds.
static int add_buffersrc(Ctx *c, AVFilterInOut *pad, enum AVMediaType type, int in_idx) {
    if (in_idx < 0 || in_idx >= c->n_in) {
        fprintf(stderr, "ffmpeg-wasi: process: filter references input %d, only %d given\n", in_idx, c->n_in);
        return AVERROR(EINVAL);
    }
    const AVCodec *dec_codec = NULL;
    int st = av_find_best_stream(c->in[in_idx], type, -1, -1, &dec_codec, 0);
    if (st < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: input %d has no %s stream\n", in_idx, av_get_media_type_string(type));
        return st;
    }

    GIn *g = &c->gin[c->n_gin];
    g->in_idx = in_idx;
    g->st_idx = st;
    g->dec = avcodec_alloc_context3(dec_codec);
    if (!g->dec) return AVERROR(ENOMEM);
    avcodec_parameters_to_context(g->dec, c->in[in_idx]->streams[st]->codecpar);
    g->dec->pkt_timebase = c->in[in_idx]->streams[st]->time_base;
    int ret = avcodec_open2(g->dec, dec_codec, NULL);
    if (ret < 0) return ret;

    char args[512];
    const AVFilter *bufsrc;
    if (type == AVMEDIA_TYPE_VIDEO) {
        bufsrc = avfilter_get_by_name("buffer");
        AVRational fr = c->in[in_idx]->streams[st]->avg_frame_rate;
        if (!fr.num) fr = c->in[in_idx]->streams[st]->r_frame_rate;
        snprintf(args, sizeof(args),
                 "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d:frame_rate=%d/%d",
                 g->dec->width, g->dec->height, g->dec->pix_fmt,
                 g->dec->pkt_timebase.num, g->dec->pkt_timebase.den,
                 g->dec->sample_aspect_ratio.num,
                 g->dec->sample_aspect_ratio.den ? g->dec->sample_aspect_ratio.den : 1,
                 fr.num, fr.den ? fr.den : 1);
    } else {
        bufsrc = avfilter_get_by_name("abuffer");
        AVChannelLayout chl;
        if (g->dec->ch_layout.order == AV_CHANNEL_ORDER_UNSPEC)
            av_channel_layout_default(&chl, g->dec->ch_layout.nb_channels);
        else
            av_channel_layout_copy(&chl, &g->dec->ch_layout);
        char lay[64];
        av_channel_layout_describe(&chl, lay, sizeof(lay));
        av_channel_layout_uninit(&chl);
        snprintf(args, sizeof(args),
                 "time_base=1/%d:sample_rate=%d:sample_fmt=%s:channel_layout=%s",
                 g->dec->sample_rate, g->dec->sample_rate,
                 av_get_sample_fmt_name(g->dec->sample_fmt), lay);
    }

    char nm[32];
    snprintf(nm, sizeof(nm), "src%d", c->n_gin);
    ret = avfilter_graph_create_filter(&g->src, bufsrc, nm, args, NULL, c->graph);
    if (ret < 0) return ret;
    ret = avfilter_link(g->src, 0, pad->filter_ctx, pad->pad_idx);
    if (ret < 0) return ret;
    c->n_gin++;
    return 0;
}

// add_buffersink wires a buffersink onto one graph output pad, constrained to a
// format the chosen encoder supports.
static int add_buffersink(Ctx *c, AVFilterInOut *pad, const char *vcodec, const char *acodec) {
    enum AVMediaType type = avfilter_pad_get_type(pad->filter_ctx->output_pads, pad->pad_idx);
    const char *enc_name = type == AVMEDIA_TYPE_VIDEO ? vcodec : acodec;
    if (!enc_name) {
        fprintf(stderr, "ffmpeg-wasi: process: graph output is %s but no codec was given for it\n",
                av_get_media_type_string(type));
        return AVERROR(EINVAL);
    }
    const AVCodec *enc_codec = avcodec_find_encoder_by_name(enc_name);
    if (!enc_codec) {
        fprintf(stderr, "ffmpeg-wasi: process: unknown encoder %s\n", enc_name);
        return AVERROR(EINVAL);
    }

    GOut *go = &c->gout[c->n_gout];
    go->type = type;
    const AVFilter *bufsink = avfilter_get_by_name(type == AVMEDIA_TYPE_VIDEO ? "buffersink" : "abuffersink");
    char nm[32];
    snprintf(nm, sizeof(nm), "sink%d", c->n_gout);
    go->sink = avfilter_graph_alloc_filter(c->graph, bufsink, nm);
    if (!go->sink) return AVERROR(ENOMEM);

    int ret = 0;
    if (type == AVMEDIA_TYPE_VIDEO && enc_codec->pix_fmts) {
        enum AVPixelFormat pf[] = {enc_codec->pix_fmts[0], AV_PIX_FMT_NONE};
        ret = av_opt_set_bin(go->sink, "pix_fmts", (const uint8_t *)pf, sizeof(pf[0]) * 2, AV_OPT_SEARCH_CHILDREN);
    } else if (type == AVMEDIA_TYPE_AUDIO && enc_codec->sample_fmts) {
        enum AVSampleFormat sf[] = {enc_codec->sample_fmts[0], AV_SAMPLE_FMT_NONE};
        ret = av_opt_set_bin(go->sink, "sample_fmts", (const uint8_t *)sf, sizeof(sf[0]) * 2, AV_OPT_SEARCH_CHILDREN);
    }
    if (ret < 0) return ret;
    ret = avfilter_init_str(go->sink, NULL);
    if (ret < 0) return ret;
    ret = avfilter_link(pad->filter_ctx, pad->pad_idx, go->sink, 0);
    if (ret < 0) return ret;
    c->n_gout++;
    return 0;
}

// open_encoder configures and opens the encoder for one graph output from the
// negotiated buffersink format, and adds the output stream.
static int open_encoder(Ctx *c, GOut *go, const char *enc_name) {
    const AVCodec *enc_codec = avcodec_find_encoder_by_name(enc_name);
    go->enc = avcodec_alloc_context3(enc_codec);
    if (!go->enc) return AVERROR(ENOMEM);

    if (go->type == AVMEDIA_TYPE_VIDEO) {
        go->enc->width = av_buffersink_get_w(go->sink);
        go->enc->height = av_buffersink_get_h(go->sink);
        go->enc->pix_fmt = av_buffersink_get_format(go->sink);
        go->enc->sample_aspect_ratio = av_buffersink_get_sample_aspect_ratio(go->sink);
        go->enc->time_base = av_buffersink_get_time_base(go->sink);
        AVRational fr = av_buffersink_get_frame_rate(go->sink);
        go->enc->framerate = fr.num ? fr : (AVRational){25, 1};
        if (!go->enc->time_base.num) go->enc->time_base = av_inv_q(go->enc->framerate);
    } else {
        go->enc->sample_fmt = av_buffersink_get_format(go->sink);
        go->enc->sample_rate = av_buffersink_get_sample_rate(go->sink);
        av_buffersink_get_ch_layout(go->sink, &go->enc->ch_layout);
        go->enc->time_base = (AVRational){1, go->enc->sample_rate};
    }
    if (c->ofmt->oformat->flags & AVFMT_GLOBALHEADER)
        go->enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

    AVDictionary *opts = NULL;
    av_dict_copy(&opts, c->enc_opts, 0);
    int ret = avcodec_open2(go->enc, enc_codec, &opts);
    av_dict_free(&opts);
    if (ret < 0) { fprintf(stderr, "ffmpeg-wasi: process: open encoder %s failed\n", enc_name); return ret; }

    if (go->type == AVMEDIA_TYPE_AUDIO && !(enc_codec->capabilities & AV_CODEC_CAP_VARIABLE_FRAME_SIZE)
        && go->enc->frame_size > 0)
        av_buffersink_set_frame_size(go->sink, go->enc->frame_size);

    go->ost = avformat_new_stream(c->ofmt, NULL);
    if (!go->ost) return AVERROR(ENOMEM);
    avcodec_parameters_from_context(go->ost->codecpar, go->enc);
    go->ost->time_base = go->enc->time_base;
    return 0;
}

static int drain_encoder(Ctx *c, GOut *go, AVFrame *frame) {
    int ret = avcodec_send_frame(go->enc, frame);
    if (ret < 0) return ret;
    AVPacket *pkt = av_packet_alloc();
    while (ret >= 0) {
        ret = avcodec_receive_packet(go->enc, pkt);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { ret = 0; break; }
        if (ret < 0) break;
        av_packet_rescale_ts(pkt, go->enc->time_base, go->ost->time_base);
        pkt->stream_index = go->ost->index;
        ret = av_interleaved_write_frame(c->ofmt, pkt);
        av_packet_unref(pkt);
    }
    av_packet_free(&pkt);
    return ret;
}

// pull_sinks drains every buffersink and encodes whatever frames are ready.
static int pull_sinks(Ctx *c) {
    AVFrame *f = av_frame_alloc();
    int ret = 0;
    for (int i = 0; i < c->n_gout; i++) {
        while (1) {
            ret = av_buffersink_get_frame(c->gout[i].sink, f);
            if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { ret = 0; break; }
            if (ret < 0) goto done;
            // The buffersink already sets frame->pts in the sink's timebase
            // (which is the encoder's), so feed it as-is — overriding it (e.g.
            // with best_effort_timestamp, a decoder concept) breaks filters like
            // xfade that synthesise their own timestamps.
            ret = drain_encoder(c, &c->gout[i], f);
            av_frame_unref(f);
            if (ret < 0) goto done;
        }
    }
done:
    av_frame_free(&f);
    return ret;
}

// feed routes one input packet to every graph input fed by that (input, stream).
static int feed(Ctx *c, int in_idx, AVPacket *pkt, AVFrame *frame) {
    int ret = 0;
    for (int i = 0; i < c->n_gin; i++) {
        GIn *g = &c->gin[i];
        if (g->in_idx != in_idx || g->st_idx != pkt->stream_index) continue;
        ret = avcodec_send_packet(g->dec, pkt);
        if (ret < 0) return ret;
        while ((ret = avcodec_receive_frame(g->dec, frame)) >= 0) {
            ret = av_buffersrc_add_frame_flags(g->src, frame, AV_BUFFERSRC_FLAG_KEEP_REF);
            av_frame_unref(frame);
            if (ret < 0) return ret;
        }
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) ret = 0;
        if (ret < 0) return ret;
    }
    return pull_sinks(c);
}

// build_default_graph generates a passthrough graph + bracketed output labels for
// input 0's streams when the spec gives no filter.
static void build_default_graph(Ctx *c, const char *vcodec, const char *acodec, char *out, size_t n) {
    out[0] = 0;
    int has_v = av_find_best_stream(c->in[0], AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0) >= 0;
    int has_a = av_find_best_stream(c->in[0], AVMEDIA_TYPE_AUDIO, -1, -1, NULL, 0) >= 0;
    char tmp[256] = "";
    if (vcodec && has_v) av_strlcatf(tmp, sizeof(tmp), "[0:v]null[v];");
    if (acodec && has_a) av_strlcatf(tmp, sizeof(tmp), "[0:a]anull[a];");
    size_t len = strlen(tmp);
    if (len && tmp[len - 1] == ';') tmp[len - 1] = 0;  // trim trailing ';'
    av_strlcpy(out, tmp, n);
}

int op_process(const cJSON *spec) {
    const cJSON *inputs = cJSON_GetObjectItemCaseSensitive(spec, "inputs");
    const cJSON *outputs = cJSON_GetObjectItemCaseSensitive(spec, "outputs");
    if (!cJSON_IsArray(inputs) || cJSON_GetArraySize(inputs) < 1 ||
        !cJSON_IsArray(outputs) || cJSON_GetArraySize(outputs) < 1) {
        fprintf(stderr, "ffmpeg-wasi: process: need at least one input and one output\n");
        return 2;
    }
    const cJSON *out0 = cJSON_GetArrayItem(outputs, 0);
    const char *out_path = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(out0, "path"));
    const char *vcodec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(out0, "video_codec"));
    const char *acodec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(out0, "audio_codec"));
    const char *filter = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "filter"));
    if (!out_path || (!vcodec && !acodec)) {
        fprintf(stderr, "ffmpeg-wasi: process: need output.path and a video and/or audio codec\n");
        return 2;
    }

    Ctx c = {0};
    int rc = 0;

    // Encoder options from output.options (strings).
    const cJSON *opts = cJSON_GetObjectItemCaseSensitive(out0, "options");
    const cJSON *o = NULL;
    cJSON_ArrayForEach(o, opts) {
        if (cJSON_IsString(o)) av_dict_set(&c.enc_opts, o->string, o->valuestring, 0);
    }

    // Open every input.
    const cJSON *in = NULL;
    cJSON_ArrayForEach(in, inputs) {
        if (c.n_in >= MAX_INPUTS) { rc = AVERROR(EINVAL); goto end; }
        const char *p = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(in, "path"));
        if ((rc = avformat_open_input(&c.in[c.n_in], p, NULL, NULL)) < 0) {
            fprintf(stderr, "ffmpeg-wasi: process: cannot open input %s\n", p ? p : "(null)"); goto end;
        }
        if ((rc = avformat_find_stream_info(c.in[c.n_in], NULL)) < 0) goto end;
        c.n_in++;
    }

    avformat_alloc_output_context2(&c.ofmt, NULL, NULL, out_path);
    if (!c.ofmt) { fprintf(stderr, "ffmpeg-wasi: process: cannot guess output format for %s\n", out_path); rc = -1; goto end; }

    // The filter graph: the spec's filter, or a generated passthrough.
    char autograph[512];
    const char *graph_str = filter;
    if (!graph_str || !*graph_str) {
        build_default_graph(&c, vcodec, acodec, autograph, sizeof(autograph));
        graph_str = autograph;
    }

    c.graph = avfilter_graph_alloc();
    AVFilterInOut *gin = NULL, *gout = NULL;
    if ((rc = avfilter_graph_parse2(c.graph, graph_str, &gin, &gout)) < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: bad filtergraph %s\n", graph_str); goto end;
    }
    // Wire a buffersrc onto each graph input pad.
    for (AVFilterInOut *p = gin; p; p = p->next) {
        int in_idx; enum AVMediaType type;
        if (parse_input_pad(p->name, &in_idx, &type) < 0) {
            fprintf(stderr, "ffmpeg-wasi: process: cannot map graph input pad %s (expected N:v / N:a)\n", p->name);
            rc = AVERROR(EINVAL); avfilter_inout_free(&gin); avfilter_inout_free(&gout); goto end;
        }
        if ((rc = add_buffersrc(&c, p, type, in_idx)) < 0) {
            avfilter_inout_free(&gin); avfilter_inout_free(&gout); goto end;
        }
    }
    // Wire a buffersink onto each graph output pad.
    for (AVFilterInOut *p = gout; p; p = p->next) {
        if ((rc = add_buffersink(&c, p, vcodec, acodec)) < 0) {
            avfilter_inout_free(&gin); avfilter_inout_free(&gout); goto end;
        }
    }
    avfilter_inout_free(&gin);
    avfilter_inout_free(&gout);

    if ((rc = avfilter_graph_config(c.graph, NULL)) < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: filtergraph config failed\n"); goto end;
    }

    // Encoders + output streams from the negotiated sink formats.
    for (int i = 0; i < c.n_gout; i++) {
        if ((rc = open_encoder(&c, &c.gout[i], c.gout[i].type == AVMEDIA_TYPE_VIDEO ? vcodec : acodec)) < 0) goto end;
    }

    if (!(c.ofmt->oformat->flags & AVFMT_NOFILE)) {
        if ((rc = avio_open(&c.ofmt->pb, out_path, AVIO_FLAG_WRITE)) < 0) {
            fprintf(stderr, "ffmpeg-wasi: process: cannot open output %s\n", out_path); goto end;
        }
    }
    if ((rc = avformat_write_header(c.ofmt, NULL)) < 0) { fprintf(stderr, "ffmpeg-wasi: write header failed\n"); goto end; }

    // Multi-input transcode loop: round-robin read, feed, drain.
    AVPacket *pkt = av_packet_alloc();
    AVFrame *frame = av_frame_alloc();
    int remaining = c.n_in;
    while (remaining > 0 && rc >= 0) {
        for (int i = 0; i < c.n_in && rc >= 0; i++) {
            if (c.eof[i]) continue;
            int r = av_read_frame(c.in[i], pkt);
            if (r < 0) {
                c.eof[i] = 1;
                remaining--;
                // Flush this input's decoders and close its buffersrcs.
                for (int gi = 0; gi < c.n_gin; gi++) {
                    if (c.gin[gi].in_idx != i) continue;
                    avcodec_send_packet(c.gin[gi].dec, NULL);
                    while (avcodec_receive_frame(c.gin[gi].dec, frame) >= 0) {
                        av_buffersrc_add_frame_flags(c.gin[gi].src, frame, AV_BUFFERSRC_FLAG_KEEP_REF);
                        av_frame_unref(frame);
                    }
                    av_buffersrc_add_frame_flags(c.gin[gi].src, NULL, 0);
                }
                rc = pull_sinks(&c);
                continue;
            }
            rc = feed(&c, i, pkt, frame);
            av_packet_unref(pkt);
        }
    }
    // Final graph drain + encoder flush.
    if (rc >= 0) rc = pull_sinks(&c);
    for (int i = 0; i < c.n_gout && rc >= 0; i++) rc = drain_encoder(&c, &c.gout[i], NULL);

    av_packet_free(&pkt);
    av_frame_free(&frame);
    if (rc >= 0) av_write_trailer(c.ofmt);

    if (rc >= 0) {
        cJSON *res = cJSON_CreateObject();
        cJSON_AddStringToObject(res, "output", out_path);
        cJSON *streams = cJSON_AddArrayToObject(res, "streams");
        for (int i = 0; i < c.n_gout; i++) {
            cJSON *s = cJSON_CreateObject();
            cJSON_AddStringToObject(s, "type", av_get_media_type_string(c.gout[i].type));
            cJSON_AddStringToObject(s, "codec", c.gout[i].enc->codec->name);
            cJSON_AddItemToArray(streams, s);
        }
        char *j = cJSON_PrintUnformatted(res);
        if (j) { printf("%s\n", j); free(j); }
        cJSON_Delete(res);
    }

end:
    if (rc < 0) {
        char buf[128];
        av_strerror(rc, buf, sizeof(buf));
        fprintf(stderr, "ffmpeg-wasi: process: %s\n", buf);
    }
    if (c.ofmt && c.ofmt->pb && !(c.ofmt->oformat->flags & AVFMT_NOFILE)) avio_closep(&c.ofmt->pb);
    for (int i = 0; i < c.n_gin; i++) avcodec_free_context(&c.gin[i].dec);
    for (int i = 0; i < c.n_gout; i++) avcodec_free_context(&c.gout[i].enc);
    if (c.graph) avfilter_graph_free(&c.graph);
    if (c.ofmt) avformat_free_context(c.ofmt);
    for (int i = 0; i < c.n_in; i++) avformat_close_input(&c.in[i]);
    av_dict_free(&c.enc_opts);
    return rc < 0 ? 1 : 0;
}
