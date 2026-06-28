// ffmpeg-wasi — the "process" operation: a single-input → single-output
// transcode, driving libav directly (decode → filter → encode → mux).
//
// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Matt Cockayne
//
// Scope (this increment): one input, one output, transcoding the video and/or
// audio stream the output requests a codec for. Each stream runs through a small
// filter graph (buffersrc → optional user filter → format-convert → buffersink),
// so the user `filter` field is a simple per-stream chain (e.g. "scale=1280:-2",
// "volume=0.5"). The full multi-pad filter_complex and multi-output muxing are
// later increments. Paths resolve against the mounted WASI filesystem.

#include <stdio.h>
#include <string.h>

#include <libavutil/avutil.h>
#include <libavutil/opt.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#include "process.h"

// A single stream's transcode pipeline.
typedef struct {
    int active;
    enum AVMediaType type;
    int in_index;            // input stream index
    AVCodecContext *dec;
    AVCodecContext *enc;
    AVFilterGraph  *graph;
    AVFilterContext *src;    // buffer / abuffer
    AVFilterContext *sink;   // buffersink / abuffersink
    AVStream *out_stream;
} Xcode;

static void xcode_free(Xcode *x) {
    if (x->graph) avfilter_graph_free(&x->graph);
    if (x->dec) avcodec_free_context(&x->dec);
    if (x->enc) avcodec_free_context(&x->enc);
    x->active = 0;
}

// build_graph wires buffersrc → (filter_spec) → buffersink, constraining the
// sink to a format the encoder accepts (libavfilter auto-inserts scale/aformat).
static int build_graph(Xcode *x, const AVCodec *enc_codec, const char *filter_spec) {
    char args[512];
    int ret = 0;
    const AVFilter *bufsrc, *bufsink;
    AVFilterInOut *outputs = avfilter_inout_alloc();
    AVFilterInOut *inputs = avfilter_inout_alloc();
    x->graph = avfilter_graph_alloc();
    if (!outputs || !inputs || !x->graph) { ret = AVERROR(ENOMEM); goto done; }

    if (x->type == AVMEDIA_TYPE_VIDEO) {
        bufsrc = avfilter_get_by_name("buffer");
        bufsink = avfilter_get_by_name("buffersink");
        snprintf(args, sizeof(args),
                 "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
                 x->dec->width, x->dec->height, x->dec->pix_fmt,
                 x->dec->pkt_timebase.num, x->dec->pkt_timebase.den,
                 x->dec->sample_aspect_ratio.num, x->dec->sample_aspect_ratio.den ? x->dec->sample_aspect_ratio.den : 1);
    } else {
        bufsrc = avfilter_get_by_name("abuffer");
        bufsink = avfilter_get_by_name("abuffersink");
        // Give the source a concrete channel layout (the input may carry an
        // unspecified one, e.g. mono WAV "1 channels", which encoders reject).
        AVChannelLayout chl;
        if (x->dec->ch_layout.order == AV_CHANNEL_ORDER_UNSPEC)
            av_channel_layout_default(&chl, x->dec->ch_layout.nb_channels);
        else
            av_channel_layout_copy(&chl, &x->dec->ch_layout);
        char layout[64];
        av_channel_layout_describe(&chl, layout, sizeof(layout));
        av_channel_layout_uninit(&chl);
        snprintf(args, sizeof(args),
                 "time_base=1/%d:sample_rate=%d:sample_fmt=%s:channel_layout=%s",
                 x->dec->sample_rate, x->dec->sample_rate,
                 av_get_sample_fmt_name(x->dec->sample_fmt), layout);
    }

    ret = avfilter_graph_create_filter(&x->src, bufsrc, "in", args, NULL, x->graph);
    if (ret < 0) goto done;

    // The sink's format constraints must be set BEFORE it is initialised, so
    // allocate it, constrain it to a format the encoder supports (libavfilter
    // then auto-inserts the scale/aformat conversion), then init.
    x->sink = avfilter_graph_alloc_filter(x->graph, bufsink, "out");
    if (!x->sink) { ret = AVERROR(ENOMEM); goto done; }
    if (x->type == AVMEDIA_TYPE_VIDEO && enc_codec->pix_fmts) {
        enum AVPixelFormat pf[] = {enc_codec->pix_fmts[0], AV_PIX_FMT_NONE};
        ret = av_opt_set_bin(x->sink, "pix_fmts", (const uint8_t *)pf,
                             sizeof(pf[0]) * 2, AV_OPT_SEARCH_CHILDREN);
    } else if (x->type == AVMEDIA_TYPE_AUDIO && enc_codec->sample_fmts) {
        enum AVSampleFormat sf[] = {enc_codec->sample_fmts[0], AV_SAMPLE_FMT_NONE};
        ret = av_opt_set_bin(x->sink, "sample_fmts", (const uint8_t *)sf,
                             sizeof(sf[0]) * 2, AV_OPT_SEARCH_CHILDREN);
    }
    if (ret < 0) goto done;
    ret = avfilter_init_str(x->sink, NULL);
    if (ret < 0) goto done;

    outputs->name = av_strdup("in");
    outputs->filter_ctx = x->src;
    outputs->pad_idx = 0;
    outputs->next = NULL;
    inputs->name = av_strdup("out");
    inputs->filter_ctx = x->sink;
    inputs->pad_idx = 0;
    inputs->next = NULL;

    ret = avfilter_graph_parse_ptr(x->graph, filter_spec, &inputs, &outputs, NULL);
    if (ret < 0) goto done;
    ret = avfilter_graph_config(x->graph, NULL);

done:
    avfilter_inout_free(&inputs);
    avfilter_inout_free(&outputs);
    return ret;
}

// setup_xcode sets up the decode→filter→encode pipeline for one media type, and
// adds the output stream. Returns 1 if set up, 0 if not applicable, <0 on error.
static int setup_xcode(Xcode *x, AVFormatContext *ifmt, AVFormatContext *ofmt,
                       enum AVMediaType type, const char *enc_name,
                       const char *filter_spec, AVDictionary **enc_opts) {
    int ret;
    const AVCodec *dec_codec = NULL;
    int idx = av_find_best_stream(ifmt, type, -1, -1, &dec_codec, 0);
    if (idx < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: no %s stream in input\n",
                av_get_media_type_string(type));
        return 0;
    }

    const AVCodec *enc_codec = avcodec_find_encoder_by_name(enc_name);
    if (!enc_codec) {
        fprintf(stderr, "ffmpeg-wasi: process: unknown encoder %s\n", enc_name);
        return AVERROR(EINVAL);
    }

    x->type = type;
    x->in_index = idx;
    x->dec = avcodec_alloc_context3(dec_codec);
    if (!x->dec) return AVERROR(ENOMEM);
    avcodec_parameters_to_context(x->dec, ifmt->streams[idx]->codecpar);
    x->dec->pkt_timebase = ifmt->streams[idx]->time_base;
    ret = avcodec_open2(x->dec, dec_codec, NULL);
    if (ret < 0) { fprintf(stderr, "ffmpeg-wasi: open decoder failed\n"); return ret; }

    ret = build_graph(x, enc_codec, filter_spec && *filter_spec ? filter_spec :
                      (type == AVMEDIA_TYPE_VIDEO ? "null" : "anull"));
    if (ret < 0) { fprintf(stderr, "ffmpeg-wasi: build filter graph failed\n"); return ret; }

    // Configure the encoder from the negotiated sink format.
    x->enc = avcodec_alloc_context3(enc_codec);
    if (!x->enc) return AVERROR(ENOMEM);
    if (type == AVMEDIA_TYPE_VIDEO) {
        x->enc->width = av_buffersink_get_w(x->sink);
        x->enc->height = av_buffersink_get_h(x->sink);
        x->enc->pix_fmt = av_buffersink_get_format(x->sink);
        x->enc->sample_aspect_ratio = av_buffersink_get_sample_aspect_ratio(x->sink);
        x->enc->time_base = av_buffersink_get_time_base(x->sink);
        AVRational fr = av_buffersink_get_frame_rate(x->sink);
        x->enc->framerate = fr.num ? fr : (AVRational){25, 1};
        if (!x->enc->time_base.num) x->enc->time_base = av_inv_q(x->enc->framerate);
    } else {
        x->enc->sample_fmt = av_buffersink_get_format(x->sink);
        x->enc->sample_rate = av_buffersink_get_sample_rate(x->sink);
        av_buffersink_get_ch_layout(x->sink, &x->enc->ch_layout);
        x->enc->time_base = (AVRational){1, x->enc->sample_rate};
    }
    if (ofmt->oformat->flags & AVFMT_GLOBALHEADER)
        x->enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

    ret = avcodec_open2(x->enc, enc_codec, enc_opts);
    if (ret < 0) { fprintf(stderr, "ffmpeg-wasi: open encoder %s failed\n", enc_name); return ret; }

    // AAC and friends need a fixed frame size from the audio sink.
    if (type == AVMEDIA_TYPE_AUDIO && !(enc_codec->capabilities & AV_CODEC_CAP_VARIABLE_FRAME_SIZE)
        && x->enc->frame_size > 0)
        av_buffersink_set_frame_size(x->sink, x->enc->frame_size);

    x->out_stream = avformat_new_stream(ofmt, NULL);
    if (!x->out_stream) return AVERROR(ENOMEM);
    avcodec_parameters_from_context(x->out_stream->codecpar, x->enc);
    x->out_stream->time_base = x->enc->time_base;
    x->active = 1;
    return 1;
}

// encode_write drains the encoder and writes packets to the muxer.
static int encode_write(Xcode *x, AVFormatContext *ofmt, AVFrame *frame) {
    int ret = avcodec_send_frame(x->enc, frame);
    if (ret < 0) return ret;
    AVPacket *pkt = av_packet_alloc();
    while (ret >= 0) {
        ret = avcodec_receive_packet(x->enc, pkt);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { ret = 0; break; }
        if (ret < 0) break;
        av_packet_rescale_ts(pkt, x->enc->time_base, x->out_stream->time_base);
        pkt->stream_index = x->out_stream->index;
        ret = av_interleaved_write_frame(ofmt, pkt);
        av_packet_unref(pkt);
    }
    av_packet_free(&pkt);
    return ret;
}

// filter_encode pushes a decoded frame through the graph and encodes the output.
static int filter_encode(Xcode *x, AVFormatContext *ofmt, AVFrame *dec_frame) {
    int ret = av_buffersrc_add_frame_flags(x->src, dec_frame, AV_BUFFERSRC_FLAG_KEEP_REF);
    if (ret < 0) return ret;
    AVFrame *filt = av_frame_alloc();
    while (1) {
        ret = av_buffersink_get_frame(x->sink, filt);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) { ret = 0; break; }
        if (ret < 0) break;
        filt->pts = filt->best_effort_timestamp;
        ret = encode_write(x, ofmt, filt);
        av_frame_unref(filt);
        if (ret < 0) break;
    }
    av_frame_free(&filt);
    return ret;
}

int op_process(const cJSON *spec) {
    const cJSON *inputs = cJSON_GetObjectItemCaseSensitive(spec, "inputs");
    const cJSON *outputs = cJSON_GetObjectItemCaseSensitive(spec, "outputs");
    const cJSON *filter = cJSON_GetObjectItemCaseSensitive(spec, "filter");
    if (!cJSON_IsArray(inputs) || cJSON_GetArraySize(inputs) < 1 ||
        !cJSON_IsArray(outputs) || cJSON_GetArraySize(outputs) < 1) {
        fprintf(stderr, "ffmpeg-wasi: process: need at least one input and one output\n");
        return 2;
    }
    const char *in_path = cJSON_GetStringValue(
        cJSON_GetObjectItemCaseSensitive(cJSON_GetArrayItem(inputs, 0), "path"));
    const cJSON *out = cJSON_GetArrayItem(outputs, 0);
    const char *out_path = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(out, "path"));
    const char *vcodec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(out, "video_codec"));
    const char *acodec = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(out, "audio_codec"));
    const char *filter_spec = cJSON_GetStringValue(filter);
    if (!in_path || !out_path || (!vcodec && !acodec)) {
        fprintf(stderr, "ffmpeg-wasi: process: need input.path, output.path, and a video/audio codec\n");
        return 2;
    }

    // Encoder options from output.options (strings).
    AVDictionary *enc_opts = NULL;
    const cJSON *opts = cJSON_GetObjectItemCaseSensitive(out, "options");
    const cJSON *o = NULL;
    cJSON_ArrayForEach(o, opts) {
        if (cJSON_IsString(o)) av_dict_set(&enc_opts, o->string, o->valuestring, 0);
    }

    int rc = 0;
    AVFormatContext *ifmt = NULL, *ofmt = NULL;
    Xcode xs[2] = {0};

    if ((rc = avformat_open_input(&ifmt, in_path, NULL, NULL)) < 0) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot open input %s\n", in_path); goto end;
    }
    if ((rc = avformat_find_stream_info(ifmt, NULL)) < 0) goto end;

    avformat_alloc_output_context2(&ofmt, NULL, NULL, out_path);
    if (!ofmt) { fprintf(stderr, "ffmpeg-wasi: process: cannot guess output format for %s\n", out_path); rc = -1; goto end; }

    if (vcodec) {
        rc = setup_xcode(&xs[0], ifmt, ofmt, AVMEDIA_TYPE_VIDEO, vcodec, filter_spec, &enc_opts);
        if (rc < 0) goto end;
    }
    if (acodec) {
        rc = setup_xcode(&xs[1], ifmt, ofmt, AVMEDIA_TYPE_AUDIO, acodec, filter_spec, &enc_opts);
        if (rc < 0) goto end;
    }
    if (!xs[0].active && !xs[1].active) {
        fprintf(stderr, "ffmpeg-wasi: process: nothing to transcode\n"); rc = -1; goto end;
    }

    if (!(ofmt->oformat->flags & AVFMT_NOFILE)) {
        if ((rc = avio_open(&ofmt->pb, out_path, AVIO_FLAG_WRITE)) < 0) {
            fprintf(stderr, "ffmpeg-wasi: process: cannot open output %s\n", out_path); goto end;
        }
    }
    if ((rc = avformat_write_header(ofmt, NULL)) < 0) { fprintf(stderr, "ffmpeg-wasi: write header failed\n"); goto end; }

    // Transcode loop.
    AVPacket *pkt = av_packet_alloc();
    AVFrame *frame = av_frame_alloc();
    while (av_read_frame(ifmt, pkt) >= 0) {
        Xcode *x = NULL;
        if (xs[0].active && pkt->stream_index == xs[0].in_index) x = &xs[0];
        else if (xs[1].active && pkt->stream_index == xs[1].in_index) x = &xs[1];
        if (x) {
            if ((rc = avcodec_send_packet(x->dec, pkt)) >= 0) {
                while ((rc = avcodec_receive_frame(x->dec, frame)) >= 0) {
                    rc = filter_encode(x, ofmt, frame);
                    av_frame_unref(frame);
                    if (rc < 0) break;
                }
                if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) rc = 0;
            }
        }
        av_packet_unref(pkt);
        if (rc < 0) break;
    }

    // Flush decoders → filters → encoders.
    for (int i = 0; i < 2 && rc >= 0; i++) {
        if (!xs[i].active) continue;
        avcodec_send_packet(xs[i].dec, NULL);
        while (avcodec_receive_frame(xs[i].dec, frame) >= 0) {
            filter_encode(&xs[i], ofmt, frame);
            av_frame_unref(frame);
        }
        av_buffersrc_add_frame_flags(xs[i].src, NULL, 0);  // flush graph
        AVFrame *filt = av_frame_alloc();
        while (av_buffersink_get_frame(xs[i].sink, filt) >= 0) {
            filt->pts = filt->best_effort_timestamp;
            encode_write(&xs[i], ofmt, filt);
            av_frame_unref(filt);
        }
        av_frame_free(&filt);
        encode_write(&xs[i], ofmt, NULL);  // flush encoder
    }

    av_write_trailer(ofmt);
    av_packet_free(&pkt);
    av_frame_free(&frame);

    // Report what was written.
    {
        cJSON *res = cJSON_CreateObject();
        cJSON_AddStringToObject(res, "output", out_path);
        cJSON *streams = cJSON_AddArrayToObject(res, "streams");
        for (int i = 0; i < 2; i++) {
            if (!xs[i].active) continue;
            cJSON *s = cJSON_CreateObject();
            cJSON_AddStringToObject(s, "type", av_get_media_type_string(xs[i].type));
            cJSON_AddStringToObject(s, "codec", xs[i].enc->codec->name);
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
    if (ofmt && ofmt->pb && !(ofmt->oformat->flags & AVFMT_NOFILE)) avio_closep(&ofmt->pb);
    if (ofmt) avformat_free_context(ofmt);
    if (ifmt) avformat_close_input(&ifmt);
    xcode_free(&xs[0]);
    xcode_free(&xs[1]);
    av_dict_free(&enc_opts);
    return rc < 0 ? 1 : 0;
}
