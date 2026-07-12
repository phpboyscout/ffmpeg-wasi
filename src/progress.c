// ffmpeg-wasi — progress side-channel (spec 0032). SPDX-License-Identifier: MIT
#include "progress.h"

#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/types.h>
#include <unistd.h>

// PROGRESS_PATH is the vfs device afmpeg serves; each written line is parsed by
// the host (afmpeg internal/vfs) and surfaced on the WithProgress channel.
#define PROGRESS_PATH "/dev/afmpeg-progress"

// EMIT_INTERVAL_US throttles emission to once per ~100 ms of MEDIA time — a
// smooth bar without a record per frame (spec 0032 D-B2).
#define EMIT_INTERVAL_US 100000

struct Progress {
    int     fd;           // -1 when disabled or the open failed (inert)
    int64_t out_time_us;  // max output pts seen, in AV_TIME_BASE units (µs)
    int64_t total_size;   // bytes muxed so far
    int64_t frames;       // video packets muxed
    int64_t last_emit_us; // out_time at the last emit
    int     emitted;      // whether any record has been written yet
};

Progress *progress_open(int enabled) {
    Progress *p = calloc(1, sizeof *p);
    if (!p) return NULL;

    p->fd = -1;
    if (enabled) {
        // Best-effort: an inert emitter (fd < 0) if the device is absent.
        p->fd = open(PROGRESS_PATH, O_WRONLY | O_CREAT | O_TRUNC, 0644);
    }

    return p;
}

static void progress_emit(Progress *p) {
    if (!p || p->fd < 0) return;

    char buf[96];
    int n = snprintf(buf, sizeof buf,
                     "{\"frame\":%lld,\"out_time_us\":%lld,\"total_size\":%lld}\n",
                     (long long)p->frames, (long long)p->out_time_us,
                     (long long)p->total_size);
    if (n <= 0) return;

    // Ignore short writes / errors — progress is best-effort and must never
    // affect the encode.
    ssize_t w = write(p->fd, buf, (size_t)n);
    (void)w;

    p->emitted = 1;
    p->last_emit_us = p->out_time_us;
}

void progress_note(Progress *p, int is_video, int64_t out_time_us, int pkt_size) {
    if (!p) return;

    if (pkt_size > 0) p->total_size += pkt_size;
    if (is_video) p->frames++;
    if (out_time_us >= 0 && out_time_us > p->out_time_us) p->out_time_us = out_time_us;

    if (p->fd < 0) return;
    if (!p->emitted || p->out_time_us - p->last_emit_us >= EMIT_INTERVAL_US) {
        progress_emit(p);
    }
}

void progress_finish(Progress *p) { progress_emit(p); }

void progress_close(Progress *p) {
    if (!p) return;
    if (p->fd >= 0) close(p->fd);
    free(p);
}
