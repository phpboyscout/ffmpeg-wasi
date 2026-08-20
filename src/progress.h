// ffmpeg-wasi — progress side-channel (spec 0032 / afmpeg spec 0031 phase B).
// SPDX-License-Identifier: MIT
#ifndef FFMPEG_WASI_PROGRESS_H
#define FFMPEG_WASI_PROGRESS_H

#include <stdint.h>

// Progress is a one-way, best-effort emitter of NDJSON progress records to
// /dev/afmpeg-progress (served by afmpeg's vfs, which parses each line onto the
// WithProgress channel). Best-effort throughout: if the device cannot be opened,
// every call is a no-op — the encode is never blocked or failed by progress I/O.
//
// WASM ONLY. With the native IPC bridge active the emitter is deliberately inert:
// the path is a vfs device, and opening it on the native target would be a plain
// libc call straight to host disk, which the driver promises not to make
// (ffmpeg-wasi#48). Native progress is spec 0033, and is not this.
typedef struct Progress Progress;

// progress_open returns an emitter. When enabled is nonzero it opens the device;
// on failure the emitter is inert. duration_us is the job's target media duration
// in AV_TIME_BASE units (µs), or 0 when unknown — it is stamped on every record so
// the host can form Fraction = out_time/duration (accurate even for a generative
// input, which has no readable source file to measure). It never returns a value
// the other calls can't accept (a NULL is tolerated by all of them).
Progress *progress_open(int enabled, int64_t duration_us);

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
