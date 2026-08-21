// ffmpeg-wasi — the OS-enforced filesystem floor (afmpeg spec 0043 D2).
// SPDX-License-Identifier: MIT
#ifndef FFMPEG_WASI_SANDBOX_H
#define FFMPEG_WASI_SANDBOX_H

// sandbox_install puts a Landlock floor under the process when the native bridge
// is serving I/O. Call it once, after argv parsing and before any job runs.
//
// Never fails the process: an unavailable kernel is reported, not fatal (spec
// 0043 OQ1). Ask sandbox_state() for what actually happened.
void sandbox_install(void);

// sandbox_state returns "landlock", "disabled" or "unavailable" — the string the
// driver reports in --capabilities and in every job result, so a host that
// requires confinement can ASSERT it rather than assume it (spec 0043 D3).
const char *sandbox_state(void);

#endif
