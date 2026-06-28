// ffmpeg-wasi — the libav-direct engine.
//
// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Matt Cockayne
//
// This is OUR engine: a small WASI program that links FFmpeg's libav* libraries
// and drives them directly — no ffmpeg CLI, no threads. The source is MIT; the
// linked *artifact* inherits libav*'s licence (LGPL by default, GPL when built
// with libx264). See the repository README for the licensing model.
//
// PHASE A (this file today): proves the build — it links libav* and reports the
// version and the available codecs/muxers/filters, confirming the engine
// compiles, links, and runs under a WASI runtime (wazero).
//
// PHASE B (next): the real engine — read a structured job spec (process | probe)
// from argv/stdin, open inputs/outputs against the mounted WASI filesystem, build
// the filter graph via avfilter_graph_parse2, run the demux→decode→filter→encode
// →mux loop, and report results. See spec 0007 (the afmpeg repo) §4.

#include <stdio.h>
#include <string.h>

#include <libavutil/avutil.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>

static void report_codec(const char *name) {
    const AVCodec *enc = avcodec_find_encoder_by_name(name);
    const AVCodec *dec = avcodec_find_decoder_by_name(name);
    printf("  %-10s encode:%-3s decode:%-3s\n",
           name, enc ? "yes" : "no", dec ? "yes" : "no");
}

int main(int argc, char **argv) {
    (void)argc;
    (void)argv;

    printf("ffmpeg-wasi engine (Phase A)\n");
    printf("ffmpeg: %s\n", av_version_info());
    printf("libavcodec %u  libavformat %u  libavfilter %u\n",
           avcodec_version(), avformat_version(), avfilter_version());

    printf("codecs:\n");
    const char *codecs[] = {"h264", "hevc", "vp9", "mjpeg", "aac", "mp3", "opus", "flac", NULL};
    for (int i = 0; codecs[i]; i++) {
        report_codec(codecs[i]);
    }

    printf("muxers:\n");
    const char *muxers[] = {"mp4", "matroska", "webm", "mp3", "wav", NULL};
    for (int i = 0; muxers[i]; i++) {
        printf("  %-10s %s\n", muxers[i], av_guess_format(muxers[i], NULL, NULL) ? "yes" : "no");
    }

    printf("filters:\n");
    const char *filters[] = {"scale", "overlay", "concat", "xfade", "amix", "alimiter", NULL};
    for (int i = 0; filters[i]; i++) {
        printf("  %-10s %s\n", filters[i], avfilter_get_by_name(filters[i]) ? "yes" : "no");
    }

    return 0;
}
