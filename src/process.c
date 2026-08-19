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
#include <libavutil/mem.h>
#include <libavutil/opt.h>
#include <libavcodec/avcodec.h>
#include <libavcodec/bsf.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#include "process.h"
#include "meta.h"
#include "nativeio.h"
#include "progress.h"

#define MAX_INPUTS 32
#define MAX_GIN 32
#define MAX_GOUT 16
#define MAX_OUTPUTS 8
#define MAX_CPY 32
#define MAX_SUB 16

// COPY_CODEC is the codec sentinel that marks a mapped stream for packet
// passthrough (no decode/encode) — mirrors ffmpeg's `-c copy` (spec 0013).
#define COPY_CODEC "copy"

// One graph input: a decoded input stream feeding a buffersrc.
typedef struct {
    int in_idx;
    int st_idx;
    AVCodecContext *dec;
    AVFilterContext *src;
    // Set once the buffersrc stops accepting frames. A filter that completes
    // before its input does -- trim, or an xfade whose transition is shorter than
    // the clips -- closes its upstream source, and every later frame from that
    // decoder has nowhere to go. That is an ordinary end of stream for this pad,
    // not a failure of the job (ffmpeg-wasi#11).
    int closed;
} GIn;

// One output file: its muxer, codecs, options, and the graph pad labels it takes.
typedef struct {
    AVFormatContext *ofmt;
    const char *path;
    const char *vcodec;
    const char *acodec;
    const char *scodec;    // subtitle encoder name, or "copy" (spec 0019); NULL → none
    const char *format;    // forced muxer name (spec 0015); NULL → guess by extension
    AVDictionary *enc_opts;
    AVDictionary *fmt_opts; // muxer options → write_header (spec 0015)
    const cJSON *map; // array of "[label]" (graph pads) / "in:type[:idx]" (copy) strings
    const cJSON *bsf; // "bitstream_filters" object: map-key → bsf name/chain or "none"
    double duration;  // -t seconds (0 = unset); mutually exclusive with end
    double end;       // -to seconds (0 = unset)
    int copy_ts;      // preserve source PTS (default 0 = zero-base the output)

    // Metadata (spec 0020): container `metadata` tags, a `chapters` passthrough
    // directive ("copy" / an input index), and `stream_metadata` — a per-map-key
    // object of {tags, language, disposition} applied to each output stream.
    const cJSON *metadata;
    const cJSON *chapters;
    const cJSON *stream_metadata;
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

// One graph input's seek-window state (spec 0014): the requested start, the
// fast/accurate mode, and the rebase offset captured from the first frame that
// survives the window (fast mode zero-bases on the keyframe actually landed on;
// accurate mode on the requested start).
typedef struct {
    int64_t rebase; // offset subtracted from frame pts (stream tb); AV_NOPTS_VALUE until captured
} GInSeek;

// One transcoded subtitle stream (spec 0019): an input subtitle track decoded to
// AVSubtitle and re-encoded to another subtitle codec, then muxed. Subtitles do
// not traverse the lavfi graph (libavfilter has no general subtitle buffersrc),
// so this is a third lane beside the graph and the copy path. A copied subtitle
// (subtitle_codec:"copy") rides the Cpy path instead.
typedef struct {
    int in_idx;
    int st_idx;
    int out_idx;
    AVCodecContext *dec;
    AVCodecContext *enc;
    AVStream *ost;
} Sub;

// One graph output pad: a buffersink encoded into a stream of output out_idx.
typedef struct {
    enum AVMediaType type;
    AVFilterContext *sink;
    AVCodecContext *enc;
    AVStream *ost;
    int out_idx;
    char label[64]; // the graph pad label, for per-stream metadata routing (0020)
} GOut;

typedef struct {
    AVFormatContext *in[MAX_INPUTS];
    int n_in;
    int eof[MAX_INPUTS];
    AVFilterGraph *graph;
    GIn gin[MAX_GIN];
    int n_gin;
    GInSeek gseek[MAX_GIN];
    GOut gout[MAX_GOUT];
    int n_gout;
    Cpy cpy[MAX_CPY];
    int n_cpy;
    Sub sub[MAX_SUB];
    int n_sub;
    Out out[MAX_OUTPUTS];
    int n_out;

    // Per-input seek window (spec 0014): the requested start in AV_TIME_BASE
    // units (AV_NOPTS_VALUE = no seek), the accurate flag, and the source-time
    // threshold past which an input is treated as EOF early (INT64_MAX = read
    // to the end). Set up before the read loop.
    int64_t seek_us[MAX_INPUTS];
    int seek_accurate[MAX_INPUTS];
    int64_t in_cutoff_us[MAX_INPUTS];

    // Per-(input, output) copy rebase offset: the first copied packet's source
    // time (us), captured on arrival and shared by every copied stream of the
    // pair so audio and video stay aligned. AV_NOPTS_VALUE until captured.
    int64_t cpy_rebase_us[MAX_INPUTS][MAX_OUTPUTS];

    // Analysis-filter output (spec 0017 §Q): the `lavfi.*` frame metadata that
    // cropdetect/blackdetect/silencedetect/ebur128/signalstats/astats/… attach,
    // collected off the sink frames into a time-series (consecutive-deduplicated
    // per key by `analysis_last`) and surfaced in the result JSON. Lazily built.
    cJSON *analysis;
    AVDictionary *analysis_last;

    // Progress side-channel (spec 0032): NULL/inert unless the job set
    // "progress":true, in which case output packets are reported to the host via
    // pmux() as they are muxed. Best-effort — never affects the encode.
    Progress *prog;
} Ctx;

// pmux muxes one finished output packet, first reporting it to the progress
// side-channel (spec 0032): the output pts (rescaled to µs) advances the media
// clock, the packet size adds to the byte total, and a video packet counts a
// frame. progress state is inert when the job did not ask for it. pkt is read
// before the write consumes it.
static int pmux(Ctx *c, AVFormatContext *ofmt, AVStream *ost, AVPacket *pkt) {
    if (c->prog) {
        int is_video = ost->codecpar->codec_type == AVMEDIA_TYPE_VIDEO;
        int64_t ot = pkt->pts != AV_NOPTS_VALUE
                         ? av_rescale_q(pkt->pts, ost->time_base, AV_TIME_BASE_Q)
                         : -1;
        progress_note(c->prog, is_video, ot, pkt->size);
    }

    return av_interleaved_write_frame(ofmt, pkt);
}

// parse_input_pad reads a "N:v" / "N:a" graph input label into an input index +
// media type, plus an optional "N:v:K" per-type stream index (spec 0024; -1 =
// best stream of that type, the default).
static int parse_input_pad(const char *name, int *in_idx, enum AVMediaType *type, int *sel) {
    char t = 0;
    *sel = -1;
    if (sscanf(name, "%d:%c:%d", in_idx, &t, sel) < 2) return -1;
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
    if (sscanf(s, "%d:%c:%d", in_idx, &t, &k) >= 2 &&
        (t == 'v' || t == 'V' || t == 'a' || t == 'A' || t == 's' || t == 'S')) {
        if (t == 'v' || t == 'V') *type = AVMEDIA_TYPE_VIDEO;
        else if (t == 'a' || t == 'A') *type = AVMEDIA_TYPE_AUDIO;
        else *type = AVMEDIA_TYPE_SUBTITLE; // spec 0019: N:s subtitle-stream maps
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
// the graph at the pad it feeds. sel selects a specific stream of the type
// (spec 0024; -1 = best).
static int add_buffersrc(Ctx *c, AVFilterInOut *pad, enum AVMediaType type, int in_idx, int sel) {
    if (c->n_gin >= MAX_GIN) {
        fprintf(stderr, "ffmpeg-wasi: process: too many graph inputs (max %d)\n", MAX_GIN);
        return AVERROR(EINVAL);
    }
    if (in_idx < 0 || in_idx >= c->n_in) {
        fprintf(stderr, "ffmpeg-wasi: process: filter references input %d, only %d given\n", in_idx, c->n_in);
        return AVERROR(EINVAL);
    }
    int st = resolve_map_stream(c->in[in_idx], type, sel);
    if (st < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: input %d has no %s stream %d\n",
                in_idx, av_get_media_type_string(type), sel);
        return st;
    }
    const AVCodec *dec_codec = avcodec_find_decoder(c->in[in_idx]->streams[st]->codecpar->codec_id);
    if (!dec_codec) {
        fprintf(stderr, "ffmpeg-wasi: process: no decoder for input %d stream %d\n", in_idx, st);
        return AVERROR_DECODER_NOT_FOUND;
    }

    int idx = c->n_gin;
    GIn *g = &c->gin[idx];
    g->in_idx = in_idx;
    g->st_idx = st;
    g->dec = avcodec_alloc_context3(dec_codec);
    if (!g->dec) return AVERROR(ENOMEM);
    c->n_gin++; // count it now so the end-cleanup frees g->dec on any error below
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
    snprintf(nm, sizeof(nm), "src%d", idx);
    ret = avfilter_graph_create_filter(&g->src, bufsrc, nm, args, NULL, c->graph);
    if (ret < 0) return ret;
    ret = avfilter_link(g->src, 0, pad->filter_ctx, pad->pad_idx);
    if (ret < 0) return ret;
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
    snprintf(go->label, sizeof(go->label), "%s", pad->name ? pad->name : ""); // for 0020 routing
    const AVFilter *bufsink = avfilter_get_by_name(type == AVMEDIA_TYPE_VIDEO ? "buffersink" : "abuffersink");
    char nm[32];
    snprintf(nm, sizeof(nm), "sink%d", c->n_gout);
    go->sink = avfilter_graph_alloc_filter(c->graph, bufsink, nm);
    if (!go->sink) return AVERROR(ENOMEM);

    // Pin the sink to the encoder's first format so the graph auto-inserts the
    // needed conversion. Use FFmpeg 8's counted-array options ("pixel_formats" /
    // "sample_formats"); the older "pix_fmts"/"sample_fmts" binary options are
    // deprecated and feed a malformed list into format negotiation that crashes on
    // an -O2 native build (merge_formats_internal), though -Oz wasm tolerated it.
    //
    // The supported list comes from avcodec_get_supported_config, not the AVCodec
    // fields it replaced: those are deprecated in FFmpeg 8 and removed outright in
    // 9.0, where AVCodec no longer carries them. A NULL list means the encoder
    // accepts everything, so pin nothing and let the graph negotiate — which is
    // exactly what the old `if (enc_codec->pix_fmts)` guard did.
    int ret = 0;
    if (type == AVMEDIA_TYPE_VIDEO) {
        const enum AVPixelFormat *pix_fmts = NULL;
        ret = avcodec_get_supported_config(NULL, enc_codec, AV_CODEC_CONFIG_PIX_FORMAT, 0,
                                           (const void **)&pix_fmts, NULL);
        if (ret >= 0 && pix_fmts) {
            enum AVPixelFormat pf = pix_fmts[0];
            ret = av_opt_set_array(go->sink, "pixel_formats", AV_OPT_SEARCH_CHILDREN, 0, 1, AV_OPT_TYPE_PIXEL_FMT, &pf);
        }
    } else if (type == AVMEDIA_TYPE_AUDIO) {
        const enum AVSampleFormat *sample_fmts = NULL;
        ret = avcodec_get_supported_config(NULL, enc_codec, AV_CODEC_CONFIG_SAMPLE_FORMAT, 0,
                                           (const void **)&sample_fmts, NULL);
        if (ret >= 0 && sample_fmts) {
            enum AVSampleFormat sf = sample_fmts[0];
            ret = av_opt_set_array(go->sink, "sample_formats", AV_OPT_SEARCH_CHILDREN, 0, 1, AV_OPT_TYPE_SAMPLE_FMT, &sf);
        }

        // Sample format alone is not enough. An encoder with a restricted set of
        // sample RATES -- libopus is the everyday case, it takes 48kHz and a few
        // others but not 44.1 -- would otherwise be handed whatever the graph
        // happened to carry and reject it, where ffmpeg auto-inserts a resample
        // (ffmpeg-wasi#17). Same for channel layouts.
        //
        // The WHOLE supported list goes to the sink rather than its first entry:
        // negotiation then keeps the input's rate when the encoder accepts it and
        // converts only when it must. Pinning one would resample every job.
        if (ret >= 0) {
            const int *rates = NULL;
            if (avcodec_get_supported_config(NULL, enc_codec, AV_CODEC_CONFIG_SAMPLE_RATE, 0,
                                             (const void **)&rates, NULL) >= 0 && rates) {
                unsigned n = 0;
                while (rates[n]) n++; // the list is terminated by a zero rate
                if (n) ret = av_opt_set_array(go->sink, "samplerates", AV_OPT_SEARCH_CHILDREN,
                                              0, n, AV_OPT_TYPE_INT, rates);
            }
        }

        if (ret >= 0) {
            const AVChannelLayout *layouts = NULL;
            if (avcodec_get_supported_config(NULL, enc_codec, AV_CODEC_CONFIG_CHANNEL_LAYOUT, 0,
                                             (const void **)&layouts, NULL) >= 0 && layouts) {
                unsigned n = 0;
                while (layouts[n].nb_channels) n++; // terminated by a zeroed layout
                if (n) ret = av_opt_set_array(go->sink, "channel_layouts", AV_OPT_SEARCH_CHILDREN,
                                              0, n, AV_OPT_TYPE_CHLAYOUT, layouts);
            }
        }
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

// job_seek_offset_us is the source offset copy_ts preserves: the first seeked
// input's start. With no seek it is 0 and copy_ts is a no-op; with several
// differently-seeked inputs feeding one graph, the first one's timeline wins
// (documented — a mixed-seek copy_ts timeline is not well-defined).
static int64_t job_seek_offset_us(const Ctx *c) {
    for (int i = 0; i < c->n_in; i++) {
        if (c->seek_us[i] != AV_NOPTS_VALUE) return c->seek_us[i];
    }
    return 0;
}

// out_cutoff_us returns where output `out` stops on its own timeline (us), or
// INT64_MAX for no window. On the default zero-based timeline `duration` and
// `end` coincide (the output starts at 0); under copy_ts the timeline is the
// source's, so `duration` counts from the seek point and `end` is an absolute
// source position (spec 0014 Q1).
static int64_t out_cutoff_us(const Ctx *c, const Out *out) {
    if (out->duration > 0) {
        int64_t base = out->copy_ts ? job_seek_offset_us(c) : 0;
        return base + (int64_t)(out->duration * AV_TIME_BASE);
    }
    if (out->end > 0) return (int64_t)(out->end * AV_TIME_BASE);
    return INT64_MAX;
}

static int drain_encoder(Ctx *c, GOut *go, AVFrame *frame) {
    Out *out = &c->out[go->out_idx];
    AVFormatContext *ofmt = out->ofmt;
    int ret = avcodec_send_frame(go->enc, frame);
    if (ret < 0) return ret;
    int64_t cutoff = out_cutoff_us(c, out);
    AVPacket *pkt = av_packet_alloc();
    if (!pkt) return AVERROR(ENOMEM);
    while (ret >= 0) {
        ret = avcodec_receive_packet(go->enc, pkt);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { ret = 0; break; }
        if (ret < 0) break;
        // copy_ts restores the source timeline the graph entered zero-based.
        if (out->copy_ts) {
            int64_t off = av_rescale_q(job_seek_offset_us(c), AV_TIME_BASE_Q, go->enc->time_base);
            if (pkt->pts != AV_NOPTS_VALUE) pkt->pts += off;
            if (pkt->dts != AV_NOPTS_VALUE) pkt->dts += off;
        }
        // Enforce the output window (-t / -to): drop packets past the cutoff.
        if (cutoff != INT64_MAX && pkt->pts != AV_NOPTS_VALUE &&
            av_rescale_q(pkt->pts, go->enc->time_base, AV_TIME_BASE_Q) >= cutoff) {
            av_packet_unref(pkt);
            continue;
        }
        // A packet with no duration becomes a zero-duration sample in MP4's
        // time-to-sample table, and a zero-duration final sample is invisible to
        // every decoder that counts frames — the file carries it but nothing plays
        // it (ffmpeg-wasi#12). Matroska hides this by falling back to a default
        // frame duration, which is why the same job kept every frame there.
        //
        // Take it from the frame where the encoder propagated one, and otherwise
        // derive it from the encoder's own frame rate. Only the LAST packet is
        // usually affected, because the muxer infers every other sample's duration
        // from the next one's timestamp.
        if (pkt->duration <= 0) {
            if (go->enc->framerate.num > 0 && go->enc->framerate.den > 0) {
                pkt->duration = av_rescale_q(1, av_inv_q(go->enc->framerate), go->enc->time_base);
            } else if (go->enc->codec_type == AVMEDIA_TYPE_AUDIO && go->enc->frame_size > 0) {
                pkt->duration = av_rescale_q(go->enc->frame_size,
                                             (AVRational){1, go->enc->sample_rate},
                                             go->enc->time_base);
            }
        }

        av_packet_rescale_ts(pkt, go->enc->time_base, go->ost->time_base);
        pkt->stream_index = go->ost->index;
        ret = pmux(c, ofmt, go->ost, pkt);
        av_packet_unref(pkt);
    }
    av_packet_free(&pkt);
    return ret;
}

// collect_analysis harvests the `lavfi.*` metadata that an analysis filter
// (cropdetect/blackdetect/silencedetect/ebur128/signalstats/astats/…) attaches to a
// frame at the sink (spec 0017 §Q) into c->analysis — a time-series of {t, key,
// value}, consecutive-deduplicated per key so a stable measurement records once while
// discrete events (silence_start/end) each record. The "lavfi." prefix is dropped.
// Advisory + best-effort: any allocation failure just skips, never blocking the job.
static void collect_analysis(Ctx *c, const AVFrame *f, AVRational tb) {
    const AVDictionaryEntry *e = NULL;
    while ((e = av_dict_iterate(f->metadata, e))) {
        if (strncmp(e->key, "lavfi.", 6) != 0) continue;

        const AVDictionaryEntry *last = av_dict_get(c->analysis_last, e->key, NULL, 0);
        if (last && strcmp(last->value, e->value) == 0) continue; // unchanged → skip
        if (av_dict_set(&c->analysis_last, e->key, e->value, 0) < 0) return;

        if (!c->analysis && !(c->analysis = cJSON_CreateArray())) return;
        cJSON *ent = cJSON_CreateObject();
        if (!ent) return;

        double t = (f->pts == AV_NOPTS_VALUE) ? -1.0 : (double)f->pts * av_q2d(tb);
        cJSON_AddNumberToObject(ent, "t", t);
        cJSON_AddStringToObject(ent, "key", e->key + 6); // drop the "lavfi." prefix
        cJSON_AddStringToObject(ent, "value", e->value);
        cJSON_AddItemToArray(c->analysis, ent);
    }
}

// pull_sinks drains every buffersink and encodes whatever frames are ready.
static int pull_sinks(Ctx *c) {
    AVFrame *f = av_frame_alloc();
    if (!f) return AVERROR(ENOMEM);
    int ret = 0;
    for (int i = 0; i < c->n_gout; i++) {
        while (1) {
            ret = av_buffersink_get_frame(c->gout[i].sink, f);
            if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { ret = 0; break; }
            if (ret < 0) goto done;
            // Harvest any analysis-filter metadata riding on this frame (spec 0017 §Q).
            collect_analysis(c, f, av_buffersink_get_time_base(c->gout[i].sink));
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

// push_frame applies the seek window to one decoded frame and feeds it into a
// graph input (spec 0014): under an accurate seek, frames before the requested
// start are discarded (the decode-and-discard that buys frame accuracy); the
// surviving stream is zero-based — fast mode on the keyframe actually landed on,
// accurate mode on the requested start — so the graph sees a clip starting at 0.
// gi indexes the GIn (its seek state lives in c->gseek[gi]).
static int push_frame(Ctx *c, int gi, AVFrame *frame) {
    GIn *g = &c->gin[gi];
    int ret;

    if (c->seek_us[g->in_idx] != AV_NOPTS_VALUE) {
        AVStream *st = c->in[g->in_idx]->streams[g->st_idx];
        int64_t t = frame->best_effort_timestamp != AV_NOPTS_VALUE
                        ? frame->best_effort_timestamp
                        : frame->pts;

        if (t != AV_NOPTS_VALUE) {
            int64_t start = av_rescale_q(c->seek_us[g->in_idx], AV_TIME_BASE_Q, st->time_base);
            if (c->seek_accurate[g->in_idx] && t < start) return 0; // decode-and-discard
            if (c->gseek[gi].rebase == AV_NOPTS_VALUE) {
                c->gseek[gi].rebase = c->seek_accurate[g->in_idx] ? start : t;
            }
            if (frame->pts != AV_NOPTS_VALUE) frame->pts = t - c->gseek[gi].rebase;
        }
    }

    if (g->closed) {           // the graph stopped taking frames from this pad
        av_frame_unref(frame);
        return 0;
    }

    ret = av_buffersrc_add_frame_flags(g->src, frame, AV_BUFFERSRC_FLAG_KEEP_REF);
    av_frame_unref(frame);

    // AVERROR_EOF here means a downstream filter has completed and closed this
    // source. Returning it would abort a job that is in fact finishing normally,
    // and -- depending on whether the input ran out first -- that abort is what
    // made the same job succeed on real files and fail over a host bridge.
    // Remember the pad is done and carry on; the remaining inputs still need to
    // be drained so the graph can produce its tail.
    if (ret == AVERROR_EOF) {
        g->closed = 1;
        return 0;
    }

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
            ret = push_frame(c, i, frame);
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
    o->scodec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "subtitle_codec"));
    o->map = cJSON_GetObjectItemCaseSensitive(spec, "map");
    o->bsf = cJSON_GetObjectItemCaseSensitive(spec, "bitstream_filters");
    if (!o->path || (!o->vcodec && !o->acodec && !o->scodec)) {
        fprintf(stderr, "ffmpeg-wasi: process: each output needs path and a video, audio and/or subtitle codec\n");
        return 2;
    }
    // The output window (spec 0014): -t seconds or -to position, not both.
    const cJSON *dur = cJSON_GetObjectItemCaseSensitive(spec, "duration");
    const cJSON *end = cJSON_GetObjectItemCaseSensitive(spec, "end");
    if (cJSON_IsNumber(dur)) o->duration = dur->valuedouble;
    if (cJSON_IsNumber(end)) o->end = end->valuedouble;
    if (o->duration > 0 && o->end > 0) {
        fprintf(stderr, "ffmpeg-wasi: process: `duration` and `end` are mutually exclusive on %s\n", o->path);
        return 2;
    }
    o->copy_ts = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(spec, "copy_ts"));
    // Metadata (spec 0020): container tags, a chapters passthrough directive, and
    // the per-stream metadata object. Parsed here, applied before write_header.
    o->metadata = cJSON_GetObjectItemCaseSensitive(spec, "metadata");
    o->chapters = cJSON_GetObjectItemCaseSensitive(spec, "chapters");
    o->stream_metadata = cJSON_GetObjectItemCaseSensitive(spec, "stream_metadata");
    // Guard the container type before walking it: a malformed but trusted spec
    // whose "options" is not an object (e.g. a string) is ignored, not iterated
    // unpredictably (spec 0027 §4C, defence-in-depth on trusted input).
    const cJSON *opts = cJSON_GetObjectItemCaseSensitive(spec, "options"), *kv = NULL;
    if (cJSON_IsObject(opts)) {
        cJSON_ArrayForEach(kv, opts) {
            if (cJSON_IsString(kv)) av_dict_set(&o->enc_opts, kv->string, kv->valuestring, 0);
        }
    }
    // Muxer options (spec 0015): a separate dict routed to write_header — segment
    // timing/naming, fragmentation flags (movflags), etc. — distinct from the
    // encoder `options` above (D-0015-B: no guessing which dict an option is for).
    const cJSON *fopts = cJSON_GetObjectItemCaseSensitive(spec, "format_options");
    if (cJSON_IsObject(fopts)) {
        cJSON_ArrayForEach(kv, fopts) {
            if (cJSON_IsString(kv)) av_dict_set(&o->fmt_opts, kv->string, kv->valuestring, 0);
        }
    }

    // `format` forces the muxer by name (spec 0015: "hls"/"dash"/"segment"/
    // "mpegts"/… — needed where the extension doesn't imply it); absent → the
    // path extension guesses (D-0015-C).
    o->format = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(spec, "format"));
    avformat_alloc_output_context2(&o->ofmt, NULL, o->format, o->path);
    if (!o->ofmt) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot resolve output format for %s%s%s\n",
                o->path, o->format ? " / " : "", o->format ? o->format : "");
        av_dict_free(&o->enc_opts); // this Out is never counted in n_out, so free here
        av_dict_free(&o->fmt_opts);
        return -1;
    }
    return 0;
}

// add_copy_descriptors scans every output's `map` for unbracketed input-stream
// specifiers ("0:v", "0:a:0", "0:0") and records a Cpy for each — the streams
// passed through verbatim rather than routed through the filter graph (spec 0013).
// add_default_copy_descriptors handles the case build_default_graph cannot: an
// output that omits `map` and asks for a copied codec. The default graph
// deliberately gives a copied stream no pad, and the explicit-map loop below
// never runs when there is no map, so between them a
// {"video_codec":"copy","audio_codec":"copy"} job with no map produced no
// streams at all and failed with "No streams to mux were specified"
// (ffmpeg-wasi#18) -- despite the job spec documenting `map` as optional for a
// single output.
//
// This mirrors what the default graph does for a transcoded codec: take input
// 0's best stream of that type, if it has one.
static int add_default_copy_descriptors(Ctx *c, int oi) {
    const struct {
        const char *codec;
        enum AVMediaType type;
    } lanes[] = {
        {c->out[oi].vcodec, AVMEDIA_TYPE_VIDEO},
        {c->out[oi].acodec, AVMEDIA_TYPE_AUDIO},
    };

    for (size_t i = 0; i < sizeof lanes / sizeof lanes[0]; i++) {
        if (!lanes[i].codec || strcmp(lanes[i].codec, COPY_CODEC) != 0) continue;

        int st = av_find_best_stream(c->in[0], lanes[i].type, -1, -1, NULL, 0);
        if (st < 0) continue; // no such stream to copy; not an error

        if (c->n_cpy >= MAX_CPY) {
            fprintf(stderr, "ffmpeg-wasi: process: too many copied streams\n");
            return AVERROR(EINVAL);
        }

        Cpy *cp = &c->cpy[c->n_cpy++];
        cp->in_idx = 0;
        cp->st_idx = st;
        cp->out_idx = oi;
        // No map entry to name it by; the bitstream-filter lookup keys off this,
        // and a job with no map cannot have named a filter for it either.
        cp->map_key = NULL;
        cp->ost = NULL;
        cp->bsf = NULL;
    }

    return 0;
}

static int add_copy_descriptors(Ctx *c) {
    for (int oi = 0; oi < c->n_out; oi++) {
        if (!c->out[oi].map || cJSON_GetArraySize(c->out[oi].map) == 0) {
            int rc = add_default_copy_descriptors(c, oi);
            if (rc < 0) return rc;
            continue;
        }

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

            // A subtitle stream with a real subtitle_codec (not "copy") is
            // transcoded on the Sub lane; everything else — and copied subtitles —
            // rides the packet-passthrough Cpy path (spec 0019 D-0019-B).
            const char *sc = c->out[oi].scodec;
            int transcode_sub = type == AVMEDIA_TYPE_SUBTITLE && sc && strcmp(sc, COPY_CODEC) != 0;
            if (transcode_sub) {
                if (c->n_sub >= MAX_SUB) { fprintf(stderr, "ffmpeg-wasi: process: too many subtitle streams\n"); return AVERROR(EINVAL); }
                Sub *su = &c->sub[c->n_sub++];
                su->in_idx = in_idx;
                su->st_idx = st;
                su->out_idx = oi;
                su->dec = su->enc = NULL;
                su->ost = NULL;
                continue;
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
    // Carry the source disposition + tags across the copy (spec 0020): an
    // attached_pic (cover art) stream must keep its flag or the muxer rejects it /
    // re-encodes a still; language and other tags ride along too.
    cp->ost->disposition = ist->disposition;
    av_dict_copy(&cp->ost->metadata, ist->metadata, 0);

    const char *bsf_spec = NULL;
    // map_key is NULL for a stream copied by default (no `map` given), and a job
    // that named no map cannot have named a bitstream filter for it either.
    if (cp->map_key && cJSON_IsObject(out->bsf)) {
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

// write_copy_pkt applies the seek/window semantics to one copied packet and
// interleaves it into the muxer (spec 0014 composed with 0013's copy path):
// the first packet of an (input, output) pair captures the rebase offset — the
// keyframe the demuxer actually landed on, shared across the pair's streams so
// audio and video stay aligned — the output window drops packets past the
// cutoff, and timestamps are zero-based unless the output preserves them
// (copy_ts). Consumes pkt on success or failure of the write.
static int write_copy_pkt(Ctx *c, Cpy *cp, AVRational src_tb, AVPacket *pkt) {
    Out *out = &c->out[cp->out_idx];
    AVStream *ost = cp->ost;
    int64_t *rebase = &c->cpy_rebase_us[cp->in_idx][cp->out_idx];

    int64_t t = pkt->pts != AV_NOPTS_VALUE ? pkt->pts : pkt->dts;
    if (t != AV_NOPTS_VALUE) {
        int64_t t_us = av_rescale_q(t, src_tb, AV_TIME_BASE_Q);
        if (*rebase == AV_NOPTS_VALUE) *rebase = t_us;

        // The window on the source timeline: duration counts from the pair's
        // start; end is absolute under copy_ts, output-relative otherwise.
        int64_t cutoff = INT64_MAX;
        if (out->duration > 0) {
            cutoff = *rebase + (int64_t)(out->duration * AV_TIME_BASE);
        } else if (out->end > 0) {
            cutoff = (out->copy_ts ? 0 : *rebase) + (int64_t)(out->end * AV_TIME_BASE);
        }
        if (t_us >= cutoff) {
            av_packet_unref(pkt);
            return 0;
        }

        if (!out->copy_ts) {
            int64_t off = av_rescale_q(*rebase, AV_TIME_BASE_Q, src_tb);
            if (pkt->pts != AV_NOPTS_VALUE) pkt->pts -= off;
            if (pkt->dts != AV_NOPTS_VALUE) pkt->dts -= off;
        }
    }

    av_packet_rescale_ts(pkt, src_tb, ost->time_base);
    pkt->stream_index = ost->index;
    pkt->pos = -1;
    return pmux(c, out->ofmt, ost, pkt);
}

// copy_one passes one source packet to a single copy target: through its BSF (a
// drain loop) or verbatim, then muxed. Works on a private ref so the same source
// packet can fan out to several copy targets and decoders.
static int copy_one(Ctx *c, Cpy *cp, const AVPacket *src) {
    AVRational src_tb = c->in[cp->in_idx]->streams[cp->st_idx]->time_base;

    AVPacket *pkt = av_packet_alloc();
    if (!pkt) return AVERROR(ENOMEM);

    int ret = av_packet_ref(pkt, src);
    if (ret < 0) { av_packet_free(&pkt); return ret; }

    if (!cp->bsf) {
        ret = write_copy_pkt(c, cp, src_tb, pkt); // consumes pkt
        av_packet_free(&pkt);
        return ret;
    }

    ret = av_bsf_send_packet(cp->bsf, pkt); // consumes pkt
    if (ret < 0) { av_packet_free(&pkt); return ret; }
    while ((ret = av_bsf_receive_packet(cp->bsf, pkt)) == 0) {
        ret = write_copy_pkt(c, cp, cp->bsf->time_base_out, pkt);
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

        // A send that fails at end of stream means the filter never drains, so
        // whatever it was holding is simply absent from the output. Discarding
        // this made that silent (ffmpeg-wasi#28).
        if ((ret = av_bsf_send_packet(cp->bsf, NULL)) < 0) break;

        AVPacket *pkt = av_packet_alloc();
        if (!pkt) return AVERROR(ENOMEM);
        while ((ret = av_bsf_receive_packet(cp->bsf, pkt)) == 0) {
            if ((ret = write_copy_pkt(c, cp, cp->bsf->time_base_out, pkt)) < 0) break;
        }
        if (ret == AVERROR_EOF) ret = 0; // the drain completing, not a failure
        av_packet_free(&pkt);
    }
    return ret;
}

// setup_sub_stream opens the decoder + encoder for one transcoded subtitle stream
// and adds its output stream (spec 0019). The encoder inherits the decoder's ASS
// subtitle_header — mov_text/ass encode from ASS-formatted events, so they need it.
static int setup_sub_stream(Ctx *c, Sub *su) {
    Out *out = &c->out[su->out_idx];
    AVStream *ist = c->in[su->in_idx]->streams[su->st_idx];

    const AVCodec *dec = avcodec_find_decoder(ist->codecpar->codec_id);
    if (!dec) { fprintf(stderr, "ffmpeg-wasi: process: no decoder for subtitle stream\n"); return AVERROR_DECODER_NOT_FOUND; }
    su->dec = avcodec_alloc_context3(dec);
    if (!su->dec) return AVERROR(ENOMEM);
    int ret = avcodec_parameters_to_context(su->dec, ist->codecpar);
    if (ret < 0) return ret;
    su->dec->pkt_timebase = ist->time_base;
    if ((ret = avcodec_open2(su->dec, dec, NULL)) < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: open subtitle decoder failed\n"); return ret;
    }

    const AVCodec *enc = avcodec_find_encoder_by_name(out->scodec);
    if (!enc || enc->type != AVMEDIA_TYPE_SUBTITLE) {
        fprintf(stderr, "ffmpeg-wasi: process: unknown subtitle encoder %s\n", out->scodec);
        return AVERROR_ENCODER_NOT_FOUND;
    }
    su->enc = avcodec_alloc_context3(enc);
    if (!su->enc) return AVERROR(ENOMEM);
    su->enc->time_base = (AVRational){1, 1000}; // subtitle ms timeline
    if (su->dec->subtitle_header && su->dec->subtitle_header_size > 0) {
        su->enc->subtitle_header = av_malloc(su->dec->subtitle_header_size + 1);
        if (!su->enc->subtitle_header) return AVERROR(ENOMEM);
        memcpy(su->enc->subtitle_header, su->dec->subtitle_header, su->dec->subtitle_header_size);
        su->enc->subtitle_header[su->dec->subtitle_header_size] = 0;
        su->enc->subtitle_header_size = su->dec->subtitle_header_size;
    }
    if (out->ofmt->oformat->flags & AVFMT_GLOBALHEADER) su->enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    if ((ret = avcodec_open2(su->enc, enc, NULL)) < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: open subtitle encoder %s failed\n", out->scodec); return ret;
    }

    su->ost = avformat_new_stream(out->ofmt, NULL);
    if (!su->ost) return AVERROR(ENOMEM);
    if ((ret = avcodec_parameters_from_context(su->ost->codecpar, su->enc)) < 0) return ret;
    su->ost->time_base = su->enc->time_base;
    return 0;
}

// write_sub_pkt encodes one decoded AVSubtitle to the output subtitle stream and
// muxes it (spec 0019). It applies the same seek/window semantics as the copy
// lane (spec 0014): the event's source time is zero-based against the input's
// seek start (unless copy_ts), and events starting past the output window are
// dropped — so transcoded subtitles stay aligned with the seeked video/audio.
static int write_sub_pkt(Ctx *c, Sub *su, AVSubtitle *sub) {
    Out *out = &c->out[su->out_idx];

    // Event start + duration on the source timeline (AV_TIME_BASE units):
    // decode_subtitle2 sets sub->pts in AV_TIME_BASE; the display times are ms.
    int64_t base_pts = sub->pts != AV_NOPTS_VALUE ? sub->pts : 0;
    int64_t start_us = base_pts + av_rescale_q(sub->start_display_time, (AVRational){1, 1000}, AV_TIME_BASE_Q);
    int64_t dur_us = av_rescale_q((int64_t)sub->end_display_time - sub->start_display_time,
                                  (AVRational){1, 1000}, AV_TIME_BASE_Q);

    int64_t seek = c->seek_us[su->in_idx];       // AV_NOPTS_VALUE when the input isn't seeked
    int64_t seek0 = seek != AV_NOPTS_VALUE ? seek : 0;

    // Output window on the source timeline — mirrors write_copy_pkt.
    int64_t cutoff = INT64_MAX;
    if (out->duration > 0) cutoff = seek0 + (int64_t)(out->duration * AV_TIME_BASE);
    else if (out->end > 0) cutoff = (out->copy_ts ? 0 : seek0) + (int64_t)(out->end * AV_TIME_BASE);
    if (start_us >= cutoff) return 0;

    // Zero-base against the seek start unless copy_ts preserves the source timeline.
    int64_t out_us = out->copy_ts ? start_us : start_us - seek0;
    if (out_us < 0) out_us = 0;

    const int bufsize = 1 << 16; // one subtitle event; ample for text formats
    uint8_t *buf = av_malloc(bufsize); // heap, not the wasm stack
    if (!buf) return AVERROR(ENOMEM);
    int n = avcodec_encode_subtitle(su->enc, buf, bufsize, sub);
    if (n <= 0) { av_free(buf); return n; } // n==0: nothing to write

    AVPacket *pkt = av_packet_alloc();
    int ret = pkt ? av_new_packet(pkt, n) : AVERROR(ENOMEM);
    if (ret < 0) { av_free(buf); av_packet_free(&pkt); return ret; }
    memcpy(pkt->data, buf, n);
    av_free(buf);

    pkt->pts = av_rescale_q(out_us, AV_TIME_BASE_Q, su->ost->time_base);
    pkt->dts = pkt->pts;
    pkt->duration = av_rescale_q(dur_us, AV_TIME_BASE_Q, su->ost->time_base);
    pkt->stream_index = su->ost->index;
    ret = pmux(c, out->ofmt, su->ost, pkt);
    av_packet_free(&pkt);
    return ret;
}

// sub_packet decodes one input subtitle packet and re-encodes it to every Sub
// target fed by that (input, stream) — the subtitle transcode lane (spec 0019).
static int sub_packet(Ctx *c, int in_idx, const AVPacket *pkt) {
    for (int i = 0; i < c->n_sub; i++) {
        Sub *su = &c->sub[i];
        if (su->in_idx != in_idx || su->st_idx != pkt->stream_index) continue;

        AVSubtitle sub;
        int got = 0;
        int ret = avcodec_decode_subtitle2(su->dec, &sub, &got, (AVPacket *)pkt);
        if (ret < 0) return ret;
        if (!got) continue;
        ret = write_sub_pkt(c, su, &sub);
        avsubtitle_free(&sub);
        if (ret < 0) return ret;
    }
    return 0;
}

// open_concat_input joins a playlist of like-codec files as one continuous input
// via the concat *demuxer* (distinct from the concat filter, which re-encodes),
// spec 0013 §3.2. The playlist + every segment open route through afio_open_concat:
// over the IPC bridge on the native backend (nothing touches host disk), or a /tmp
// scratch list opened on real paths on wasm. safe=0 permits the segment paths — the
// playlist comes from the trusted job spec, not the untrusted media.
static int open_concat_input(AVFormatContext **out, const cJSON *concat, int idx) {
    int n = cJSON_GetArraySize(concat);
    if (n <= 0) return AVERROR(EINVAL);

    const char **segs = av_calloc((size_t)n, sizeof(*segs));
    if (!segs) return AVERROR(ENOMEM);

    int i = 0;
    const cJSON *seg = NULL;
    cJSON_ArrayForEach(seg, concat) {
        const char *s = cJSON_GetStringValue(seg);
        if (!s) { av_free(segs); return AVERROR(EINVAL); }
        segs[i++] = s;
    }

    int rc = afio_open_concat(out, segs, n, idx);
    av_free(segs);
    if (rc < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot open concat playlist\n");
        return rc;
    }
    // Past this point the context exists, so every failure has to free it: the
    // caller only frees the inputs it has counted (ffmpeg-wasi#27).
    rc = avformat_find_stream_info(*out, NULL);
    if (rc < 0) avformat_close_input(out);
    return rc;
}

// open_one_input opens a single `inputs[]` entry: a concat playlist when it has a
// `concat` array, otherwise a plain `path`.
static int open_one_input(AVFormatContext **out, const cJSON *in, int idx) {
    const cJSON *concat = cJSON_GetObjectItemCaseSensitive(in, "concat");
    if (cJSON_IsArray(concat) && cJSON_GetArraySize(concat) > 0) {
        return open_concat_input(out, concat, idx);
    }

    const char *p = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(in, "path"));

    // Forced demuxer (spec 0024): `format` names the demuxer (e.g. "rawvideo",
    // "s16le", "mp4"); absent → auto-probe. Needed for headerless/raw inputs.
    const AVInputFormat *ifmt = NULL;
    const char *fmt_name = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(in, "format"));
    if (fmt_name) {
        ifmt = av_find_input_format(fmt_name);
        if (!ifmt) {
            fprintf(stderr, "ffmpeg-wasi: process: unknown input format %s\n", fmt_name);
            return AVERROR_DEMUXER_NOT_FOUND;
        }
    }

    // Demuxer options (spec 0024): the `options` dict reaches avformat_open_input
    // (raw geometry — video_size/pixel_format/framerate, sample_rate, … — rides
    // here). Unconsumed keys are a typed error (Q2: fail loud on a wrong option).
    AVDictionary *opts = NULL;
    const cJSON *od = cJSON_GetObjectItemCaseSensitive(in, "options"), *kv = NULL;
    if (cJSON_IsObject(od)) {
        cJSON_ArrayForEach(kv, od) {
            if (cJSON_IsString(kv)) av_dict_set(&opts, kv->string, kv->valuestring, 0);
        }
    }

    int rc = afio_open_input(out, p, ifmt, &opts);
    if (rc < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot open input %s\n", p ? p : "(null)");
        av_dict_free(&opts);
        return rc;
    }
    if (av_dict_count(opts) > 0) {
        const AVDictionaryEntry *e = av_dict_iterate(opts, NULL);
        fprintf(stderr, "ffmpeg-wasi: process: input %d: unknown demuxer option %s\n", idx, e ? e->key : "?");
        av_dict_free(&opts);
        avformat_close_input(out); // opened above (ffmpeg-wasi#27)
        return AVERROR(EINVAL);
    }
    av_dict_free(&opts);

    rc = avformat_find_stream_info(*out, NULL);
    if (rc < 0) avformat_close_input(out);
    return rc;
}

// copy_chapters deep-copies an input's chapters onto an output context (spec 0020
// D-0020-B): id/time_base/start/end + the title dict, so a transcode keeps its
// chapter list. Passthrough only — authoring chapters from the spec is deferred.
static int copy_chapters(AVFormatContext *dst, const AVFormatContext *src) {
    for (unsigned i = 0; i < src->nb_chapters; i++) {
        const AVChapter *sc = src->chapters[i];
        AVChapter *dc = av_mallocz(sizeof(*dc));
        if (!dc) return AVERROR(ENOMEM);
        dc->id = sc->id;
        dc->time_base = sc->time_base;
        dc->start = sc->start;
        dc->end = sc->end;
        av_dict_copy(&dc->metadata, sc->metadata, 0);
        AVChapter **tmp = av_realloc_array(dst->chapters, dst->nb_chapters + 1, sizeof(*tmp));
        if (!tmp) { av_dict_free(&dc->metadata); av_free(dc); return AVERROR(ENOMEM); }
        dst->chapters = tmp;
        dst->chapters[dst->nb_chapters++] = dc;
    }
    return 0;
}

// stream_meta_for finds the stream_metadata entry whose key matches map_key,
// bracket-insensitively (the same routing as graph pads): a copied stream passes
// its "0:a" specifier, an encoded pad its label "vout" against a "[vout]" key.
static const cJSON *stream_meta_for(const Out *out, const char *map_key) {
    if (!cJSON_IsObject(out->stream_metadata) || !map_key) return NULL;

    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, out->stream_metadata) {
        if (e->string && label_matches(e->string, map_key)) return e;
    }
    return NULL;
}

// apply_stream_metadata applies one stream_metadata entry to an output stream:
// language + arbitrary tags onto ost->metadata, and the disposition set. Overrides
// whatever a copy carried across (spec 0020 D-0020-D).
static void apply_stream_metadata(const cJSON *meta, AVStream *ost) {
    if (!cJSON_IsObject(meta)) return;

    const char *lang = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(meta, "language"));
    if (lang) av_dict_set(&ost->metadata, "language", lang, 0);
    meta_apply_tags(&ost->metadata, cJSON_GetObjectItemCaseSensitive(meta, "tags"));
    const cJSON *disp = cJSON_GetObjectItemCaseSensitive(meta, "disposition");
    if (cJSON_IsArray(disp)) ost->disposition = meta_disposition_from_json(disp);
}

// apply_output_metadata threads one output's container tags, chapter passthrough,
// and per-stream metadata into its muxer just before write_header (spec 0020).
static int apply_output_metadata(Ctx *c, int oi) {
    Out *out = &c->out[oi];
    meta_apply_tags(&out->ofmt->metadata, out->metadata);

    // chapters: "copy" carries input 0's; an explicit index picks another; absent
    // or "none" drops them.
    const char *ch = cJSON_GetStringValue(out->chapters);
    if (ch && strcmp(ch, "none") != 0) {
        int src = strcmp(ch, "copy") == 0 ? 0 : atoi(ch);
        if (src >= 0 && src < c->n_in) {
            int rc = copy_chapters(out->ofmt, c->in[src]);
            if (rc < 0) return rc;
        }
    }

    for (int i = 0; i < c->n_gout; i++) {
        if (c->gout[i].out_idx == oi)
            apply_stream_metadata(stream_meta_for(out, c->gout[i].label), c->gout[i].ost);
    }
    for (int i = 0; i < c->n_cpy; i++) {
        if (c->cpy[i].out_idx == oi)
            apply_stream_metadata(stream_meta_for(out, c->cpy[i].map_key), c->cpy[i].ost);
    }
    return 0;
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
    // A malformed request is tracked separately from rc, NOT returned through it.
    // rc carries libav return codes, and libav uses positive values for success —
    // avformat_write_header answers AVSTREAM_INIT_IN_INIT_OUTPUT (1) when the codec
    // was already initialised. Propagating rc as the exit code would let that 1
    // surface as "processing failure" on a job that worked.
    int malformed = 0;

    // Parse every output (muxer + codecs + options + map).
    const cJSON *os = NULL;
    cJSON_ArrayForEach(os, outputs) {
        if (c.n_out >= MAX_OUTPUTS) { fprintf(stderr, "ffmpeg-wasi: process: too many outputs\n"); rc = AVERROR(EINVAL); goto end; }
        if ((rc = parse_output(&c.out[c.n_out], os)) != 0) { malformed = 1; goto end; }
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

    // Open every input (a plain path, or a concat-demuxer playlist) and apply
    // its seek window (spec 0014): parse `seek {start, mode}` and fast-seek the
    // demuxer to the keyframe at-or-before start — the packets before it are
    // never read. Accurate mode additionally decode-and-discards up to the exact
    // start (push_frame).
    const cJSON *in = NULL;
    cJSON_ArrayForEach(in, inputs) {
        if (c.n_in >= MAX_INPUTS) { rc = AVERROR(EINVAL); goto end; }
        int ii = c.n_in;
        if ((rc = open_one_input(&c.in[ii], in, ii)) < 0) goto end;
        // Count it the moment it is open. Everything below here can still fail,
        // and the cleanup at `end` only frees the inputs below n_in — so
        // incrementing at the bottom leaked the context on every one of those
        // paths (ffmpeg-wasi#27).
        c.n_in = ii + 1;

        c.seek_us[ii] = AV_NOPTS_VALUE;
        const cJSON *sk = cJSON_GetObjectItemCaseSensitive(in, "seek");
        if (cJSON_IsObject(sk)) {
            const cJSON *start = cJSON_GetObjectItemCaseSensitive(sk, "start");
            const char *mode = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(sk, "mode"));
            if (!cJSON_IsNumber(start) || start->valuedouble < 0) {
                fprintf(stderr, "ffmpeg-wasi: process: input %d seek needs a non-negative `start`\n", ii);
                rc = AVERROR(EINVAL); goto end;
            }
            if (mode && strcmp(mode, "fast") != 0 && strcmp(mode, "accurate") != 0) {
                fprintf(stderr, "ffmpeg-wasi: process: input %d seek mode %s (want fast|accurate)\n", ii, mode);
                rc = AVERROR(EINVAL); goto end;
            }
            c.seek_us[ii] = (int64_t)(start->valuedouble * AV_TIME_BASE);
            c.seek_accurate[ii] = mode && strcmp(mode, "accurate") == 0;
            if ((rc = avformat_seek_file(c.in[ii], -1, INT64_MIN,
                                         c.seek_us[ii], c.seek_us[ii], 0)) < 0) {
                fprintf(stderr, "ffmpeg-wasi: process: cannot seek input %d to %.3fs\n",
                        ii, start->valuedouble);
                goto end;
            }
        }
    }

    // Copy streams: unbracketed map entries ("0:v") pass through verbatim,
    // bypassing the graph/decoder/encoder (spec 0013).
    if ((rc = add_copy_descriptors(&c)) < 0) goto end;

    // An accurate seek needs the decode-and-discard a copied stream skips, so it
    // is a hard error on a copy — the contract states the keyframe-only rule
    // rather than silently producing an off-by-a-GOP cut (spec 0014 D-0014-F).
    for (int i = 0; i < c.n_cpy; i++) {
        if (c.seek_accurate[c.cpy[i].in_idx]) {
            fprintf(stderr, "ffmpeg-wasi: process: accurate seek cannot apply to copied stream %s (copy cuts on keyframes; use mode \"fast\")\n",
                    c.cpy[i].map_key);
            rc = AVERROR(EINVAL); goto end;
        }
        c.cpy_rebase_us[c.cpy[i].in_idx][c.cpy[i].out_idx] = AV_NOPTS_VALUE;
    }

    // Early-EOF thresholds: when every output consuming an input is windowed,
    // stop reading that input once its packets pass the largest cutoff (plus a
    // margin for interleave) instead of demuxing to the end. Any unwindowed
    // consumer keeps the input unbounded.
    for (int i = 0; i < c.n_in; i++) {
        int64_t max_cut = 0;
        int unbounded = 0;
        for (int j = 0; j < c.n_out; j++) {
            // Conservatively treat every output as a consumer of every input:
            // graph pads may mix inputs, so per-output wiring is not tracked.
            const Out *o = &c.out[j];
            if (o->duration <= 0 && o->end <= 0) { unbounded = 1; break; }
            int64_t base = (c.seek_us[i] != AV_NOPTS_VALUE) ? c.seek_us[i] : 0;
            int64_t cut = o->duration > 0
                              ? base + (int64_t)(o->duration * AV_TIME_BASE)
                              : (o->copy_ts ? 0 : base) + (int64_t)(o->end * AV_TIME_BASE);
            if (cut > max_cut) max_cut = cut;
        }
        c.in_cutoff_us[i] = unbounded ? INT64_MAX : max_cut + AV_TIME_BASE; // +1s margin
    }

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
            int in_idx, sel; enum AVMediaType type;
            // An unlabelled open input pad (name == NULL) can't be routed to a
            // source stream — reject it cleanly rather than deref it in sscanf.
            if (!p->name || parse_input_pad(p->name, &in_idx, &type, &sel) < 0) {
                fprintf(stderr, "ffmpeg-wasi: process: cannot map graph input pad %s (expected N:v / N:a / N:v:K)\n",
                        p->name ? p->name : "(unlabelled)");
                rc = AVERROR(EINVAL); avfilter_inout_free(&gin); avfilter_inout_free(&gout); goto end;
            }
            if ((rc = add_buffersrc(&c, p, type, in_idx, sel)) < 0) {
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

    // Transcoded subtitle streams (spec 0019): decoder + encoder + output stream.
    for (int i = 0; i < c.n_sub; i++) {
        if ((rc = setup_sub_stream(&c, &c.sub[i])) < 0) goto end;
    }

    // Open + write the header for every output (each may have AVFMT_NOFILE).
    for (int i = 0; i < c.n_out; i++) {
        // Thread metadata/chapters/per-stream tags into the muxer first (0020).
        if ((rc = apply_output_metadata(&c, i)) < 0) goto end;
        if (!(c.out[i].ofmt->oformat->flags & AVFMT_NOFILE)) {
            if ((rc = afio_open_output(c.out[i].ofmt, c.out[i].path)) < 0) {
                fprintf(stderr, "ffmpeg-wasi: process: cannot open output %s\n", c.out[i].path); goto end;
            }
        }
        // Muxer options (spec 0015) drive segmenting/fragmentation here; a
        // segmenting muxer (NOFILE) opens its own child files during write_header.
        if ((rc = avformat_write_header(c.out[i].ofmt, &c.out[i].fmt_opts)) < 0) {
            fprintf(stderr, "ffmpeg-wasi: process: write header for %s failed\n", c.out[i].path); goto end;
        }
    }

    // Target media duration (µs) for the host's Fraction = out_time/duration,
    // best-effort. An explicit output window (-t / -to) bounds it authoritatively —
    // this is what gives a generative/lavfi input (no readable source file) a real
    // fraction; otherwise fall back to the longest input container duration, less
    // any seek start. 0 when nothing is known (the host then keeps its byte ratio).
    int64_t prog_dur_us = 0;
    for (int j = 0; j < c.n_out; j++) {
        const Out *o = &c.out[j];
        int64_t d = 0;
        if (o->duration > 0) d = (int64_t)(o->duration * AV_TIME_BASE);
        else if (o->end > 0) d = (int64_t)(o->end * AV_TIME_BASE);
        if (d > prog_dur_us) prog_dur_us = d;
    }
    if (prog_dur_us <= 0) {
        for (int i = 0; i < c.n_in; i++) {
            if (!c.in[i] || c.in[i]->duration == AV_NOPTS_VALUE || c.in[i]->duration <= 0) continue;
            int64_t seek = (c.seek_us[i] != AV_NOPTS_VALUE) ? c.seek_us[i] : 0;
            int64_t eff = c.in[i]->duration - seek;
            if (eff > prog_dur_us) prog_dur_us = eff;
        }
    }

    // Progress side-channel (spec 0032): the host serves /dev/afmpeg-progress and
    // sets "progress":true only when it wants live progress (and the engine is
    // v9+), so opening it is best-effort — an inert emitter otherwise.
    c.prog = progress_open(cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(spec, "progress")), prog_dur_us);

    // Seek-window state for each graph input (rebase captured on first frame).
    for (int i = 0; i < c.n_gin; i++) c.gseek[i].rebase = AV_NOPTS_VALUE;

    // Multi-input transcode loop: round-robin read, feed, drain. An input whose
    // packets have passed every consuming output's window is EOF'd early rather
    // than demuxed to the end (spec 0014).
    AVPacket *pkt = av_packet_alloc();
    AVFrame *frame = av_frame_alloc();
    // A null here is out of memory, not a bad job. Carrying on dereferences it in
    // av_read_frame, and the caller cannot tell that segfault from a crash in the
    // media it handed us (ffmpeg-wasi#26).
    if (!pkt || !frame) {
        av_packet_free(&pkt);
        av_frame_free(&frame);
        rc = AVERROR(ENOMEM);
        goto end;
    }
    int remaining = c.n_in;
    while (remaining > 0 && rc >= 0) {
        for (int i = 0; i < c.n_in && rc >= 0; i++) {
            if (c.eof[i]) continue;
            int r = av_read_frame(c.in[i], pkt);
            if (r >= 0 && c.in_cutoff_us[i] != INT64_MAX) {
                int64_t t = pkt->dts != AV_NOPTS_VALUE ? pkt->dts : pkt->pts;
                if (t != AV_NOPTS_VALUE &&
                    av_rescale_q(t, c.in[i]->streams[pkt->stream_index]->time_base,
                                 AV_TIME_BASE_Q) > c.in_cutoff_us[i]) {
                    av_packet_unref(pkt);
                    r = AVERROR_EOF; // past every window: treat as end of input
                }
            }
            if (r < 0) {
                c.eof[i] = 1;
                remaining--;
                // Flush this input's decoders and close its buffersrcs.
                for (int gi = 0; gi < c.n_gin; gi++) {
                    if (c.gin[gi].in_idx != i) continue;
                    avcodec_send_packet(c.gin[gi].dec, NULL);
                    while (avcodec_receive_frame(c.gin[gi].dec, frame) >= 0) {
                        if ((rc = push_frame(&c, gi, frame)) < 0) break;
                    }
                    if (rc < 0) break;
                    // Signalling EOF on a source the graph already closed is
                    // pointless and returns AVERROR_EOF; skip it.
                    if (!c.gin[gi].closed) {
                        av_buffersrc_add_frame_flags(c.gin[gi].src, NULL, 0);
                        c.gin[gi].closed = 1;
                    }
                }
                if (rc >= 0) rc = pull_sinks(&c);
                continue;
            }
            rc = feed(&c, i, pkt, frame);
            if (rc >= 0) rc = copy_packet(&c, i, pkt); // passthrough, independent of decode
            if (rc >= 0) rc = sub_packet(&c, i, pkt);  // subtitle transcode lane (spec 0019)
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
        // The trailer is where a non-fragmented MP4 seeks back and patches its
        // moov/mdat. Discarding its return meant a failure there produced a
        // corrupt file and exit 0 (ffmpeg-wasi#16).
        for (int i = 0; i < c.n_out; i++) {
            int tr = av_write_trailer(c.out[i].ofmt);
            if (tr < 0 && rc >= 0) {
                char eb[128];
                av_strerror(tr, eb, sizeof(eb));
                fprintf(stderr, "ffmpeg-wasi: process: writing the trailer for %s: %s\n",
                        c.out[i].path, eb);
                rc = tr;
            }
        }
    }

    if (rc >= 0) {
        cJSON *res = cJSON_CreateObject();
        cJSON *outs = cJSON_AddArrayToObject(res, "outputs");
        for (int i = 0; i < c.n_out; i++) {
            cJSON *od = cJSON_CreateObject();
            cJSON_AddStringToObject(od, "path", c.out[i].path);
            // A segmenting/NOFILE muxer (hls/dash/segment) wrote a set of files —
            // `path` is the playlist/manifest; the segments are on the fs by
            // pattern (spec 0015 Q1: marker, not an enumerated child list).
            if (c.out[i].ofmt->oformat->flags & AVFMT_NOFILE)
                cJSON_AddBoolToObject(od, "segmented", 1);
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
            for (int j = 0; j < c.n_sub; j++) {
                if (c.sub[j].out_idx != i) continue;
                cJSON *s = cJSON_CreateObject();
                cJSON_AddStringToObject(s, "type", "subtitle");
                cJSON_AddStringToObject(s, "codec", c.sub[j].enc->codec->name);
                cJSON_AddItemToArray(streams, s);
            }
            cJSON_AddItemToArray(outs, od);
        }
        // Analysis-filter measurements (spec 0017 §Q), when any filter emitted them;
        // transfer ownership to the result (cleared so the cleanup below won't double-free).
        if (c.analysis) { cJSON_AddItemToObject(res, "analysis", c.analysis); c.analysis = NULL; }
        char *j = cJSON_PrintUnformatted(res);
        if (j) { printf("%s\n", j); free(j); }
        cJSON_Delete(res);
    }

end:
    // Final progress record (spec 0032), then release the emitter. Both are
    // NULL-safe and no-ops when progress was not requested; a final emit on an
    // error path is harmless (best-effort).
    progress_finish(c.prog);
    progress_close(c.prog);

    if (rc < 0) {
        char buf[128];
        av_strerror(rc, buf, sizeof(buf));
        fprintf(stderr, "ffmpeg-wasi: process: %s\n", buf);
    }
    for (int i = 0; i < c.n_out; i++) {
        if (c.out[i].ofmt && c.out[i].ofmt->pb && !(c.out[i].ofmt->oformat->flags & AVFMT_NOFILE))
            afio_close_output(c.out[i].ofmt);
        if (c.out[i].ofmt) avformat_free_context(c.out[i].ofmt);
        av_dict_free(&c.out[i].enc_opts);
        av_dict_free(&c.out[i].fmt_opts);
    }
    for (int i = 0; i < c.n_gin; i++) avcodec_free_context(&c.gin[i].dec);
    for (int i = 0; i < c.n_gout; i++) avcodec_free_context(&c.gout[i].enc);
    for (int i = 0; i < c.n_cpy; i++) av_bsf_free(&c.cpy[i].bsf); // ost is owned by its ofmt
    for (int i = 0; i < c.n_sub; i++) { // ost is owned by its ofmt
        avcodec_free_context(&c.sub[i].dec);
        avcodec_free_context(&c.sub[i].enc);
    }
    if (c.graph) avfilter_graph_free(&c.graph);
    for (int i = 0; i < c.n_in; i++) afio_close_input(&c.in[i]);
    cJSON_Delete(c.analysis); // NULL-safe; non-NULL only on an error path before emission
    av_dict_free(&c.analysis_last);
    // A malformed request is 2 ("fix the spec"), a runtime failure 1 ("the request
    // was fine, the work could not be done"). Before this distinction was made the
    // parse_output rejections above returned 2 into rc and were flattened to 0 —
    // a host keying on the exit code read a rejected job as a success that happened
    // to produce no files.
    if (malformed) return 2;
    return rc < 0 ? 1 : 0;
}
