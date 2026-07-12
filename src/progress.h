// ffmpeg-wasi — progress side-channel (spec 0032 / afmpeg spec 0031 phase B).
// SPDX-License-Identifier: MIT
#ifndef FFMPEG_WASI_PROGRESS_H
#define FFMPEG_WASI_PROGRESS_H

#include <stdint.h>

// Progress is a one-way, best-effort emitter of NDJSON progress records to
// /dev/afmpeg-progress (served by afmpeg's vfs, which parses each line onto the
// WithProgress channel). Best-effort throughout: if the device cannot be opened,
// every call is a no-op — the encode is never blocked or failed by progress I/O.
typedef struct Progress Progress;

// progress_open returns an emitter. When enabled is nonzero it opens the device;
// on failure the emitter is inert. It never returns a value the other calls can't
// accept (a NULL is tolerated by all of them).
Progress *progress_open(int enabled);

// progress_note records one muxed output packet: is_video counts toward `frame`,
// out_time_us (or -1 when unknown) advances the media clock, pkt_size adds to the
// byte total. A record is written at most once per ~100 ms of media time.
void progress_note(Progress *p, int is_video, int64_t out_time_us, int pkt_size);

// progress_finish writes a final record reflecting the completed job.
void progress_finish(Progress *p);

// progress_close releases the emitter (records are written unbuffered, so there
// is nothing to flush).
void progress_close(Progress *p);

#endif
