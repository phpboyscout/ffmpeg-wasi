// ffmpeg-wasi — the "process" operation: transcode/filter/mux driving libav
// directly, over the mounted WASI filesystem.
//
// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Matt Cockayne
//
// v3: full filter_complex into multiple outputs. N inputs feed one filter graph
// (parsed by avfilter_graph_parse2) whose labelled output pads ([vout], [aout],
// …) are each encoded and muxed. Each entry in `outputs` is its own file with
// its own codecs/options; its `map` lists the graph pad labels that go into it.
// A pad is routed to the output whose `map` names it. With a single output and
// no `map`, every pad is muxed into it (and with no `filter`, a passthrough
// graph is generated for input 0's streams) — the simple single-file case.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavutil/avutil.h>
#include <libavutil/avstring.h>
#include <libavutil/opt.h>
#include <libavcodec/avcodec.h>
#include <libavcodec/bsf.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#include "process.h"

#define MAX_INPUTS 32
#define MAX_GIN 32
#define MAX_GOUT 16
#define MAX_OUTPUTS 8
#define MAX_CPY 32

// COPY_CODEC is the codec sentinel that marks a mapped stream for packet
// passthrough (no decode/encode) — mirrors ffmpeg's `-c copy` (spec 0013).
#define COPY_CODEC "copy"

// One graph input: a decoded input stream feeding a buffersrc.
typedef struct {
    int in_idx;
    int st_idx;
    AVCodecContext *dec;
    AVFilterContext *src;
} GIn;

// One output file: its muxer, codecs, options, and the graph pad labels it takes.
typedef struct {
    AVFormatContext *ofmt;
    const char *path;
    const char *vcodec;
    const char *acodec;
    AVDictionary *enc_opts;
    const cJSON *map; // array of "[label]" (graph pads) / "in:type[:idx]" (copy) strings
    const cJSON *bsf; // "bitstream_filters" object: map-key → bsf name/chain or "none"
} Out;

// One copied stream: an input (input, stream) wired straight to an output stream,
// bypassing the decode/filter/encode path — read → optional bitstream filter →
// interleaved mux (spec 0013). map_key is the "in:type[:idx]" entry it came from,
// used to look up an explicit bitstream-filter override.
typedef struct {
    int in_idx;
    int st_idx;
    int out_idx;
    const char *map_key;
    AVStream *ost;
    AVBSFContext *bsf; // NULL → passthrough (any container-required BSF is the muxer's job)
} Cpy;

// One graph output pad: a buffersink encoded into a stream of output out_idx.
typedef struct {
    enum AVMediaType type;
    AVFilterContext *sink;
    AVCodecContext *enc;
    AVStream *ost;
    int out_idx;
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
    Cpy cpy[MAX_CPY];
    int n_cpy;
    Out out[MAX_OUTPUTS];
    int n_out;
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

// is_graph_pad reports whether a `map` entry is a bracketed graph-pad label
// ("[vout]") — as opposed to an unbracketed input-stream specifier ("0:v").
static int is_graph_pad(const char *entry) {
    return entry && entry[0] == '[';
}

// parse_map_stream reads an unbracketed input-stream specifier into an input
// index + a selector. Forms: "N:v"/"N:a" (best stream of that type), "N:v:K"
// (the K-th stream of that type), and "N:D" (absolute stream index D). Returns 0
// on a match, -1 for a bracketed pad or an unparseable entry. On return either
// *type is set (with *sel the type-relative index, -1 for best) or *type is
// UNKNOWN and *sel is the absolute stream index.
static int parse_map_stream(const char *s, int *in_idx, enum AVMediaType *type, int *sel) {
    if (is_graph_pad(s)) return -1;

    char t = 0;
    int k = -1;
    if (sscanf(s, "%d:%c:%d", in_idx, &t, &k) >= 2 && (t == 'v' || t == 'V' || t == 'a' || t == 'A')) {
        *type = (t == 'v' || t == 'V') ? AVMEDIA_TYPE_VIDEO : AVMEDIA_TYPE_AUDIO;
        *sel = k;
        return 0;
    }

    int n = 0, d = 0;
    if (sscanf(s, "%d:%d", &n, &d) == 2) {
        *in_idx = n;
        *type = AVMEDIA_TYPE_UNKNOWN;
        *sel = d;
        return 0;
    }

    return -1;
}

// resolve_map_stream turns a parsed selector into a concrete stream index within
// the input: an absolute index (type UNKNOWN), the best stream of a type
// (sel < 0), or the sel-th stream of a type.
static int resolve_map_stream(AVFormatContext *fmt, enum AVMediaType type, int sel) {
    if (type == AVMEDIA_TYPE_UNKNOWN) {
        return (sel >= 0 && (unsigned)sel < fmt->nb_streams) ? sel : AVERROR_STREAM_NOT_FOUND;
    }
    if (sel < 0) return av_find_best_stream(fmt, type, -1, -1, NULL, 0);

    int count = 0;
    for (unsigned i = 0; i < fmt->nb_streams; i++) {
        if (fmt->streams[i]->codecpar->codec_type == type && count++ == sel) return (int)i;
    }
    return AVERROR_STREAM_NOT_FOUND;
}

// label_matches reports whether a `map` entry (optionally bracketed, "[vout]")
// names the graph pad label ("vout").
static int label_matches(const char *map_entry, const char *pad_label) {
    const char *s = map_entry;
    size_t len = strlen(map_entry);
    if (len >= 2 && s[0] == '[' && s[len - 1] == ']') { s++; len -= 2; }
    return strlen(pad_label) == len && strncmp(s, pad_label, len) == 0;
}

// find_output_for_pad routes a graph output pad to an output file: the one whose
// `map` names the label, or — for a single output with no `map` — output 0.
static int find_output_for_pad(Ctx *c, const char *label) {
    for (int i = 0; i < c->n_out; i++) {
        const cJSON *m = NULL;
        cJSON_ArrayForEach(m, c->out[i].map) {
            if (cJSON_IsString(m) && label_matches(m->valuestring, label)) return i;
        }
    }
    if (c->n_out == 1 && (!c->out[0].map || cJSON_GetArraySize(c->out[0].map) == 0)) return 0;
    return -1;
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

// add_buffersink wires a buffersink onto one graph output pad, routed to the
// output file that maps it and constrained to a format that output's encoder
// supports.
static int add_buffersink(Ctx *c, AVFilterInOut *pad) {
    enum AVMediaType type = avfilter_pad_get_type(pad->filter_ctx->output_pads, pad->pad_idx);

    int out_idx = find_output_for_pad(c, pad->name ? pad->name : "");
    if (out_idx < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: graph output pad [%s] is not mapped to any output\n",
                pad->name ? pad->name : "?");
        return AVERROR(EINVAL);
    }
    const Out *out = &c->out[out_idx];
    const char *enc_name = type == AVMEDIA_TYPE_VIDEO ? out->vcodec : out->acodec;
    if (!enc_name) {
        fprintf(stderr, "ffmpeg-wasi: process: pad [%s] is %s but output %s gives no codec for it\n",
                pad->name ? pad->name : "?", av_get_media_type_string(type), out->path);
        return AVERROR(EINVAL);
    }
    const AVCodec *enc_codec = avcodec_find_encoder_by_name(enc_name);
    if (!enc_codec) {
        fprintf(stderr, "ffmpeg-wasi: process: unknown encoder %s\n", enc_name);
        return AVERROR(EINVAL);
    }

    GOut *go = &c->gout[c->n_gout];
    go->type = type;
    go->out_idx = out_idx;
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
// negotiated buffersink format, and adds the output stream to its muxer.
static int open_encoder(Ctx *c, GOut *go) {
    Out *out = &c->out[go->out_idx];
    const char *enc_name = go->type == AVMEDIA_TYPE_VIDEO ? out->vcodec : out->acodec;
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
    if (out->ofmt->oformat->flags & AVFMT_GLOBALHEADER)
        go->enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

    AVDictionary *opts = NULL;
    av_dict_copy(&opts, out->enc_opts, 0);
    int ret = avcodec_open2(go->enc, enc_codec, &opts);
    av_dict_free(&opts);
    if (ret < 0) { fprintf(stderr, "ffmpeg-wasi: process: open encoder %s failed\n", enc_name); return ret; }

    if (go->type == AVMEDIA_TYPE_AUDIO && !(enc_codec->capabilities & AV_CODEC_CAP_VARIABLE_FRAME_SIZE)
        && go->enc->frame_size > 0)
        av_buffersink_set_frame_size(go->sink, go->enc->frame_size);

    go->ost = avformat_new_stream(out->ofmt, NULL);
    if (!go->ost) return AVERROR(ENOMEM);
    avcodec_parameters_from_context(go->ost->codecpar, go->enc);
    go->ost->time_base = go->enc->time_base;
    return 0;
}

static int drain_encoder(Ctx *c, GOut *go, AVFrame *frame) {
    AVFormatContext *ofmt = c->out[go->out_idx].ofmt;
    int ret = avcodec_send_frame(go->enc, frame);
    if (ret < 0) return ret;
    AVPacket *pkt = av_packet_alloc();
    while (ret >= 0) {
        ret = avcodec_receive_packet(go->enc, pkt);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { ret = 0; break; }
        if (ret < 0) break;
        av_packet_rescale_ts(pkt, go->enc->time_base, go->ost->time_base);
        pkt->stream_index = go->ost->index;
        ret = av_interleaved_write_frame(ofmt, pkt);
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
// input 0's streams when the spec gives no filter. Used only for the single-output
// case, so it reads output 0's codecs.
static void build_default_graph(Ctx *c, char *out, size_t n) {
    out[0] = 0;
    const char *vcodec = c->out[0].vcodec, *acodec = c->out[0].acodec;
    int has_v = av_find_best_stream(c->in[0], AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0) >= 0;
    int has_a = av_find_best_stream(c->in[0], AVMEDIA_TYPE_AUDIO, -1, -1, NULL, 0) >= 0;
    // A copied stream (codec == "copy") bypasses the graph, so it gets no pad.
    int copy_v = vcodec && strcmp(vcodec, COPY_CODEC) == 0;
    int copy_a = acodec && strcmp(acodec, COPY_CODEC) == 0;
    char tmp[256] = "";
    if (vcodec && !copy_v && has_v) av_strlcatf(tmp, sizeof(tmp), "[0:v]null[v];");
    if (acodec && !copy_a && has_a) av_strlcatf(tmp, sizeof(tmp), "[0:a]anull[a];");
    size_t len = strlen(tmp);
    if (len && tmp[len - 1] == ';') tmp[len - 1] = 0;  // trim trailing ';'
    av_strlcpy(out, tmp, n);
}

// parse_output reads one `outputs[]` entry into an Out + allocates its muxer.
static int parse_output(Out *o, const cJSON *spec) {
    o->path = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "path"));
    o->vcodec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "video_codec"));
    o->acodec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "audio_codec"));
    o->map = cJSON_GetObjectItemCaseSensitive(spec, "map");
    o->bsf = cJSON_GetObjectItemCaseSensitive(spec, "bitstream_filters");
    if (!o->path || (!o->vcodec && !o->acodec)) {
        fprintf(stderr, "ffmpeg-wasi: process: each output needs path and a video and/or audio codec\n");
        return 2;
    }
    // Guard the container type before walking it: a malformed but trusted spec
    // whose "options" is not an object (e.g. a string) is ignored, not iterated
    // unpredictably (spec 0027 §4C, defence-in-depth on trusted input).
    const cJSON *opts = cJSON_GetObjectItemCaseSensitive(spec, "options"), *kv = NULL;
    if (cJSON_IsObject(opts)) {
        cJSON_ArrayForEach(kv, opts) {
            if (cJSON_IsString(kv)) av_dict_set(&o->enc_opts, kv->string, kv->valuestring, 0);
        }
    }
    avformat_alloc_output_context2(&o->ofmt, NULL, NULL, o->path);
    if (!o->ofmt) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot guess output format for %s\n", o->path);
        return -1;
    }
    return 0;
}

// add_copy_descriptors scans every output's `map` for unbracketed input-stream
// specifiers ("0:v", "0:a:0", "0:0") and records a Cpy for each — the streams
// passed through verbatim rather than routed through the filter graph (spec 0013).
static int add_copy_descriptors(Ctx *c) {
    for (int oi = 0; oi < c->n_out; oi++) {
        const cJSON *m = NULL;
        cJSON_ArrayForEach(m, c->out[oi].map) {
            if (!cJSON_IsString(m) || is_graph_pad(m->valuestring)) continue;
            if (c->n_cpy >= MAX_CPY) {
                fprintf(stderr, "ffmpeg-wasi: process: too many copied streams\n");
                return AVERROR(EINVAL);
            }

            int in_idx = 0, sel = 0;
            enum AVMediaType type = AVMEDIA_TYPE_UNKNOWN;
            if (parse_map_stream(m->valuestring, &in_idx, &type, &sel) < 0) {
                fprintf(stderr, "ffmpeg-wasi: process: cannot parse map entry %s\n", m->valuestring);
                return AVERROR(EINVAL);
            }
            if (in_idx < 0 || in_idx >= c->n_in) {
                fprintf(stderr, "ffmpeg-wasi: process: map %s references input %d, only %d given\n",
                        m->valuestring, in_idx, c->n_in);
                return AVERROR(EINVAL);
            }

            int st = resolve_map_stream(c->in[in_idx], type, sel);
            if (st < 0) {
                fprintf(stderr, "ffmpeg-wasi: process: map %s selects no stream\n", m->valuestring);
                return st;
            }

            Cpy *cp = &c->cpy[c->n_cpy++];
            cp->in_idx = in_idx;
            cp->st_idx = st;
            cp->out_idx = oi;
            cp->map_key = m->valuestring;
            cp->ost = NULL;
            cp->bsf = NULL;
        }
    }
    return 0;
}

// setup_copy_stream creates the output stream for one copied input stream, copies
// its codec parameters, and — if the output's `bitstream_filters` names one for
// this map key — allocates and initialises that BSF (taking its adjusted
// parameters onto the output stream). "none" force-disables; an absent entry
// leaves any container-required BSF to the muxer's own auto-insertion.
static int setup_copy_stream(Ctx *c, Cpy *cp) {
    Out *out = &c->out[cp->out_idx];
    AVStream *ist = c->in[cp->in_idx]->streams[cp->st_idx];

    cp->ost = avformat_new_stream(out->ofmt, NULL);
    if (!cp->ost) return AVERROR(ENOMEM);

    int ret = avcodec_parameters_copy(cp->ost->codecpar, ist->codecpar);
    if (ret < 0) return ret;
    cp->ost->codecpar->codec_tag = 0; // let the muxer pick a tag valid for its container
    cp->ost->time_base = ist->time_base;

    const char *bsf_spec = NULL;
    if (cJSON_IsObject(out->bsf)) {
        const cJSON *b = cJSON_GetObjectItemCaseSensitive(out->bsf, cp->map_key);
        if (cJSON_IsString(b)) bsf_spec = b->valuestring;
    }
    if (!bsf_spec || strcmp(bsf_spec, "none") == 0) return 0;

    ret = av_bsf_list_parse_str(bsf_spec, &cp->bsf);
    if (ret < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: bad bitstream filter %s\n", bsf_spec);
        return ret;
    }
    if ((ret = avcodec_parameters_copy(cp->bsf->par_in, ist->codecpar)) < 0) return ret;
    cp->bsf->time_base_in = ist->time_base;
    if ((ret = av_bsf_init(cp->bsf)) < 0) return ret;
    if ((ret = avcodec_parameters_copy(cp->ost->codecpar, cp->bsf->par_out)) < 0) return ret;
    cp->ost->codecpar->codec_tag = 0;
    return 0;
}

// write_copy_pkt rescales a copied packet to the output timebase and interleaves
// it into the muxer (which takes ownership of pkt).
static int write_copy_pkt(AVFormatContext *ofmt, AVStream *ost, AVRational src_tb, AVPacket *pkt) {
    av_packet_rescale_ts(pkt, src_tb, ost->time_base);
    pkt->stream_index = ost->index;
    pkt->pos = -1;
    return av_interleaved_write_frame(ofmt, pkt);
}

// copy_one passes one source packet to a single copy target: through its BSF (a
// drain loop) or verbatim, then muxed. Works on a private ref so the same source
// packet can fan out to several copy targets and decoders.
static int copy_one(Ctx *c, Cpy *cp, const AVPacket *src) {
    AVFormatContext *ofmt = c->out[cp->out_idx].ofmt;
    AVRational src_tb = c->in[cp->in_idx]->streams[cp->st_idx]->time_base;

    AVPacket *pkt = av_packet_alloc();
    if (!pkt) return AVERROR(ENOMEM);

    int ret = av_packet_ref(pkt, src);
    if (ret < 0) { av_packet_free(&pkt); return ret; }

    if (!cp->bsf) {
        ret = write_copy_pkt(ofmt, cp->ost, src_tb, pkt); // consumes pkt
        av_packet_free(&pkt);
        return ret;
    }

    ret = av_bsf_send_packet(cp->bsf, pkt); // consumes pkt
    if (ret < 0) { av_packet_free(&pkt); return ret; }
    while ((ret = av_bsf_receive_packet(cp->bsf, pkt)) == 0) {
        ret = write_copy_pkt(ofmt, cp->ost, cp->bsf->time_base_out, pkt);
        if (ret < 0) break;
    }
    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) ret = 0;
    av_packet_free(&pkt);
    return ret;
}

// copy_packet routes one input packet to every copy target fed by that
// (input, stream) — independent of the decode path, so a stream may be both
// copied and filtered (a tee).
static int copy_packet(Ctx *c, int in_idx, const AVPacket *pkt) {
    for (int i = 0; i < c->n_cpy; i++) {
        if (c->cpy[i].in_idx != in_idx || c->cpy[i].st_idx != pkt->stream_index) continue;
        int ret = copy_one(c, &c->cpy[i], pkt);
        if (ret < 0) return ret;
    }
    return 0;
}

// flush_copy_bsfs drains any bitstream filters at end of stream (send NULL), so a
// BSF that buffers (e.g. extradata extraction) emits its tail before the trailer.
static int flush_copy_bsfs(Ctx *c) {
    int ret = 0;
    for (int i = 0; i < c->n_cpy && ret >= 0; i++) {
        Cpy *cp = &c->cpy[i];
        if (!cp->bsf) continue;

        AVFormatContext *ofmt = c->out[cp->out_idx].ofmt;
        av_bsf_send_packet(cp->bsf, NULL);

        AVPacket *pkt = av_packet_alloc();
        if (!pkt) return AVERROR(ENOMEM);
        while (av_bsf_receive_packet(cp->bsf, pkt) == 0) {
            if ((ret = write_copy_pkt(ofmt, cp->ost, cp->bsf->time_base_out, pkt)) < 0) break;
        }
        av_packet_free(&pkt);
    }
    return ret;
}

// open_concat_input joins a playlist of like-codec files as one continuous input
// via the concat *demuxer* (distinct from the concat filter, which re-encodes).
// It materialises an ffconcat list in the /tmp scratch overlay referencing each
// segment by absolute path, then opens it with the concat demuxer (spec 0013 §3.2).
// safe=0 permits the absolute paths — the playlist comes from the trusted job
// spec, not the untrusted media.
static int open_concat_input(AVFormatContext **out, const cJSON *concat, int idx) {
    char listpath[64];
    snprintf(listpath, sizeof(listpath), "/tmp/afmpeg-concat-%d.txt", idx);

    FILE *lf = fopen(listpath, "w");
    if (!lf) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot create concat list\n");
        return AVERROR(EIO);
    }
    const cJSON *seg = NULL;
    cJSON_ArrayForEach(seg, concat) {
        const char *s = cJSON_GetStringValue(seg);
        if (!s) { fclose(lf); return AVERROR(EINVAL); }
        fprintf(lf, "file '/%s'\n", s[0] == '/' ? s + 1 : s); // absolute → location-independent
    }
    fclose(lf);

    const AVInputFormat *cf = av_find_input_format("concat");
    if (!cf) {
        fprintf(stderr, "ffmpeg-wasi: process: concat demuxer not built\n");
        return AVERROR_DEMUXER_NOT_FOUND;
    }

    AVDictionary *opts = NULL;
    av_dict_set(&opts, "safe", "0", 0);
    av_dict_set(&opts, "protocol_whitelist", "file,pipe", 0);
    int rc = avformat_open_input(out, listpath, cf, &opts);
    av_dict_free(&opts);
    if (rc < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot open concat playlist\n");
        return rc;
    }
    return avformat_find_stream_info(*out, NULL);
}

// open_one_input opens a single `inputs[]` entry: a concat playlist when it has a
// `concat` array, otherwise a plain `path`.
static int open_one_input(AVFormatContext **out, const cJSON *in, int idx) {
    const cJSON *concat = cJSON_GetObjectItemCaseSensitive(in, "concat");
    if (cJSON_IsArray(concat) && cJSON_GetArraySize(concat) > 0) {
        return open_concat_input(out, concat, idx);
    }

    const char *p = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(in, "path"));
    int rc = avformat_open_input(out, p, NULL, NULL);
    if (rc < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot open input %s\n", p ? p : "(null)");
        return rc;
    }
    return avformat_find_stream_info(*out, NULL);
}

int op_process(const cJSON *spec) {
    const cJSON *inputs = cJSON_GetObjectItemCaseSensitive(spec, "inputs");
    const cJSON *outputs = cJSON_GetObjectItemCaseSensitive(spec, "outputs");
    if (!cJSON_IsArray(inputs) || cJSON_GetArraySize(inputs) < 1 ||
        !cJSON_IsArray(outputs) || cJSON_GetArraySize(outputs) < 1) {
        fprintf(stderr, "ffmpeg-wasi: process: need at least one input and one output\n");
        return 2;
    }
    const char *filter = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "filter"));

    Ctx c = {0};
    int rc = 0;

    // Parse every output (muxer + codecs + options + map).
    const cJSON *os = NULL;
    cJSON_ArrayForEach(os, outputs) {
        if (c.n_out >= MAX_OUTPUTS) { fprintf(stderr, "ffmpeg-wasi: process: too many outputs\n"); rc = AVERROR(EINVAL); goto end; }
        if ((rc = parse_output(&c.out[c.n_out], os)) != 0) goto end;
        c.n_out++;
    }
    if (c.n_out > 1) {
        for (int i = 0; i < c.n_out; i++) {
            if (!c.out[i].map || cJSON_GetArraySize(c.out[i].map) == 0) {
                fprintf(stderr, "ffmpeg-wasi: process: with multiple outputs each must set `map`\n");
                rc = AVERROR(EINVAL); goto end;
            }
        }
    }

    // Open every input (a plain path, or a concat-demuxer playlist).
    const cJSON *in = NULL;
    cJSON_ArrayForEach(in, inputs) {
        if (c.n_in >= MAX_INPUTS) { rc = AVERROR(EINVAL); goto end; }
        if ((rc = open_one_input(&c.in[c.n_in], in, c.n_in)) < 0) goto end;
        c.n_in++;
    }

    // Copy streams: unbracketed map entries ("0:v") pass through verbatim,
    // bypassing the graph/decoder/encoder (spec 0013).
    if ((rc = add_copy_descriptors(&c)) < 0) goto end;

    // The filter graph: the spec's filter, or a generated passthrough. A pure-copy
    // (remux) job needs no graph — build/parse it only when a pad is required (an
    // empty default graph means every requested stream is copied).
    char autograph[512];
    const char *graph_str = filter;
    if (!graph_str || !*graph_str) {
        build_default_graph(&c, autograph, sizeof(autograph));
        graph_str = autograph;
    }

    if (graph_str && *graph_str) {
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
        // Wire a buffersink onto each graph output pad, routed to its mapped output.
        for (AVFilterInOut *p = gout; p; p = p->next) {
            if (c.n_gout >= MAX_GOUT) { rc = AVERROR(EINVAL); avfilter_inout_free(&gin); avfilter_inout_free(&gout); goto end; }
            if ((rc = add_buffersink(&c, p)) < 0) {
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
            if ((rc = open_encoder(&c, &c.gout[i])) < 0) goto end;
        }
    }

    // Copy output streams (params + optional bitstream filter), added to their
    // muxers before write_header alongside any encoded streams.
    for (int i = 0; i < c.n_cpy; i++) {
        if ((rc = setup_copy_stream(&c, &c.cpy[i])) < 0) goto end;
    }

    // Open + write the header for every output (each may have AVFMT_NOFILE).
    for (int i = 0; i < c.n_out; i++) {
        if (!(c.out[i].ofmt->oformat->flags & AVFMT_NOFILE)) {
            if ((rc = avio_open(&c.out[i].ofmt->pb, c.out[i].path, AVIO_FLAG_WRITE)) < 0) {
                fprintf(stderr, "ffmpeg-wasi: process: cannot open output %s\n", c.out[i].path); goto end;
            }
        }
        if ((rc = avformat_write_header(c.out[i].ofmt, NULL)) < 0) {
            fprintf(stderr, "ffmpeg-wasi: process: write header for %s failed\n", c.out[i].path); goto end;
        }
    }

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
            if (rc >= 0) rc = copy_packet(&c, i, pkt); // passthrough, independent of decode
            av_packet_unref(pkt);
        }
    }
    // Final graph drain + encoder flush + bitstream-filter flush.
    if (rc >= 0) rc = pull_sinks(&c);
    for (int i = 0; i < c.n_gout && rc >= 0; i++) rc = drain_encoder(&c, &c.gout[i], NULL);
    if (rc >= 0) rc = flush_copy_bsfs(&c);

    av_packet_free(&pkt);
    av_frame_free(&frame);
    if (rc >= 0) {
        for (int i = 0; i < c.n_out; i++) av_write_trailer(c.out[i].ofmt);
    }

    if (rc >= 0) {
        cJSON *res = cJSON_CreateObject();
        cJSON *outs = cJSON_AddArrayToObject(res, "outputs");
        for (int i = 0; i < c.n_out; i++) {
            cJSON *od = cJSON_CreateObject();
            cJSON_AddStringToObject(od, "path", c.out[i].path);
            cJSON *streams = cJSON_AddArrayToObject(od, "streams");
            for (int j = 0; j < c.n_gout; j++) {
                if (c.gout[j].out_idx != i) continue;
                cJSON *s = cJSON_CreateObject();
                cJSON_AddStringToObject(s, "type", av_get_media_type_string(c.gout[j].type));
                cJSON_AddStringToObject(s, "codec", c.gout[j].enc->codec->name);
                cJSON_AddItemToArray(streams, s);
            }
            for (int j = 0; j < c.n_cpy; j++) {
                if (c.cpy[j].out_idx != i) continue;
                cJSON *s = cJSON_CreateObject();
                cJSON_AddStringToObject(s, "type", av_get_media_type_string(c.cpy[j].ost->codecpar->codec_type));
                cJSON_AddStringToObject(s, "codec", avcodec_get_name(c.cpy[j].ost->codecpar->codec_id));
                cJSON_AddStringToObject(s, "disposition", "copy");
                cJSON_AddItemToArray(streams, s);
            }
            cJSON_AddItemToArray(outs, od);
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
    for (int i = 0; i < c.n_out; i++) {
        if (c.out[i].ofmt && c.out[i].ofmt->pb && !(c.out[i].ofmt->oformat->flags & AVFMT_NOFILE))
            avio_closep(&c.out[i].ofmt->pb);
        if (c.out[i].ofmt) avformat_free_context(c.out[i].ofmt);
        av_dict_free(&c.out[i].enc_opts);
    }
    for (int i = 0; i < c.n_gin; i++) avcodec_free_context(&c.gin[i].dec);
    for (int i = 0; i < c.n_gout; i++) avcodec_free_context(&c.gout[i].enc);
    for (int i = 0; i < c.n_cpy; i++) av_bsf_free(&c.cpy[i].bsf); // ost is owned by its ofmt
    if (c.graph) avfilter_graph_free(&c.graph);
    for (int i = 0; i < c.n_in; i++) avformat_close_input(&c.in[i]);
    return rc < 0 ? 1 : 0;
}
