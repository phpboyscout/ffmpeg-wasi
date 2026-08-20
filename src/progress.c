// ffmpeg-wasi — progress side-channel (spec 0032). SPDX-License-Identifier: MIT
#include "progress.h"

#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/types.h>
#include <unistd.h>

#include "nativeio.h"

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
    int64_t duration_us;  // job's target media duration (µs), 0 when unknown
    int64_t last_emit_us; // out_time at the last emit
    int     emitted;      // whether any record has been written yet
};

Progress *progress_open(int enabled, int64_t duration_us) {
    Progress *p = calloc(1, sizeof *p);
    if (!p) return NULL;

    p->fd = -1;
    p->duration_us = duration_us > 0 ? duration_us : 0;
    // With the native bridge active this open would go STRAIGHT TO HOST DISK.
    // It is a plain libc call — nothing in nativeio.c sees it — so on a driver
    // whose /dev is writable (a container, or running as root) it creates and
    // truncates a real file, against a documented guarantee that the driver
    // touches no host disk (ffmpeg-wasi#48).
    //
    // Progress is a wasm-target feature: the path is a vfs device afmpeg serves,
    // and the native backend is still on phase A (spec 0033). So the honest
    // behaviour when bridged is an inert emitter, chosen deliberately rather than
    // arrived at because the open happened to fail.
    if (enabled && !afio_bridge_active()) {
        // Best-effort: an inert emitter (fd < 0) if the device is absent.
        p->fd = open(PROGRESS_PATH, O_WRONLY | O_CREAT | O_TRUNC, 0644);
    }

    return p;
}

static void progress_emit(Progress *p) {
    if (!p || p->fd < 0) return;

    char buf[160];
    int n = snprintf(buf, sizeof buf,
                     "{\"frame\":%lld,\"out_time_us\":%lld,\"total_size\":%lld,\"duration_us\":%lld}\n",
                     (long long)p->frames, (long long)p->out_time_us,
                     (long long)p->total_size, (long long)p->duration_us);
    if (n <= 0 || n >= (int)sizeof buf) return; // truncated is not a record

    // Progress must never fail the encode, so a write error is still swallowed.
    // What changed is that a FAILED or SHORT write no longer counts as a record
    // emitted: marking it emitted put a truncated line on the channel and could
    // suppress the next update until the interval came round again, or lose the
    // final record entirely (ffmpeg-wasi#48).
    //
    // A short write also means the channel is now carrying half a JSON line, so
    // the emitter goes inert rather than appending a second half-line to it.
    ssize_t w = write(p->fd, buf, (size_t)n);
    if (w != n) {
        close(p->fd);
        p->fd = -1;
        return;
    }

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
