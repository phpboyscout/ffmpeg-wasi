// ffmpeg-wasi — job-spec option bounds (afmpeg spec 0044 D3).
// SPDX-License-Identifier: MIT
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavutil/error.h>

#include "specopts.h"

// SPEC_OPTION_CEILING is the largest iteration count a job spec may ask a
// component for.
//
// Some demuxer options make a component walk candidate paths. Today each
// candidate is a host `access()`; under afmpeg spec 0043 D1 every one becomes a
// bridge round trip, measured at 110us. `start_number_range` reaches INT_MAX,
// which is roughly 66 hours of round trips producing no output and never ending
// -- and nothing for a containment test to catch, because nothing escapes
// (ffmpeg-wasi#60).
//
// Three orders of magnitude above observed need: a realistic 500-image sequence
// takes about 43 probes, because find_image_range gallops rather than scanning.
// At 110us this bounds the worst case to roughly 1.1 seconds.
#define SPEC_OPTION_CEILING 10000

// The bounded set, and why it is this short: `avio_check` has three call sites
// in all of libavformat and libavfilter, and all three are in img2dec.c. Every
// other component that opens paths on its own initiative is a network protocol,
// and the build passes --disable-network.
//
// `start_number` is deliberately absent: it moves where a scan starts, not how
// long it runs.
static const char *const bounded[] = {
    "start_number_range", // image2  -- default 5,  flat scan for the first index
    "recursion_depth",    // concat  -- default 10, nested playlists, one open per level
};

int spec_option_in_bounds(const char *op, const char *key, const char *value) {
    if (!key || !value) return 0;

    int watched = 0;
    for (size_t i = 0; i < sizeof bounded / sizeof *bounded; i++) {
        if (strcmp(key, bounded[i]) == 0) { watched = 1; break; }
    }
    if (!watched) return 0;

    // A value libav would reject anyway is left to libav: refusing it here would
    // duplicate a diagnostic and could diverge from it.
    errno = 0;
    char *end = NULL;
    long long v = strtoll(value, &end, 10);
    if (end == value || (end && *end) || errno == ERANGE) return 0;

    if (v > SPEC_OPTION_CEILING) {
        // Refused by name rather than clamped. A clamp changes what the job means
        // without saying so -- the caller asked to scan a range and silently got a
        // different one, which is the defect #54 was raised for in another costume.
        fprintf(stderr,
                "ffmpeg-wasi: %s: %s is %lld, above the %d this engine will "
                "iterate. Each step can cost a round trip over the native "
                "bridge, so an unbounded scan is a job that produces nothing and "
                "never ends. Lower it, or name the files you want directly.\n",
                op ? op : "process", key, v, SPEC_OPTION_CEILING);
        return AVERROR(EINVAL);
    }
    return 0;
}
