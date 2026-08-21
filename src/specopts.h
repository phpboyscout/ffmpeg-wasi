// ffmpeg-wasi — job-spec option bounds (afmpeg spec 0044 D3).
// SPDX-License-Identifier: MIT
#ifndef FFMPEG_WASI_SPECOPTS_H
#define FFMPEG_WASI_SPECOPTS_H

// spec_option_in_bounds checks one job-spec option against the iteration
// ceiling. Returns 0 when the option is acceptable, or a negative AVERROR after
// printing a diagnostic naming the key.
//
// `op` names the operation for the message ("process", "probe", "frames").
int spec_option_in_bounds(const char *op, const char *key, const char *value);

#endif
