// ffmpeg-wasi — the "frames" operation (still-frame extraction).
// SPDX-License-Identifier: MIT
#ifndef FFMPEG_WASI_FRAMES_H
#define FFMPEG_WASI_FRAMES_H

#include "third_party/cJSON/cJSON.h"

// op_frames executes a {"op":"frames", inputs, select, path, codec, scale, count}
// job spec: pull one or more still frames from a single video input to templated
// image files by one of four selectors (timestamp | timestamps | interval |
// scene), each optionally scaled, and report each written file as JSON on stdout.
// Returns 0 on success, non-zero on error (with a message on stderr). Spec 0021.
int op_frames(const cJSON *spec);

#endif
