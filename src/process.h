// ffmpeg-wasi — the "process" operation (transcode/filter/mux).
// SPDX-License-Identifier: MIT
#ifndef FFMPEG_WASI_PROCESS_H
#define FFMPEG_WASI_PROCESS_H

#include "third_party/cJSON/cJSON.h"

// op_process executes a {"op":"process", inputs, filter, outputs} job spec:
// single input -> single output, transcoding the video and/or audio stream
// through an optional per-stream filter. Returns 0 on success, non-zero on
// error (with a message on stderr).
int op_process(const cJSON *spec);

#endif
