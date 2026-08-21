package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// Bounded demuxer options — afmpeg spec 0044 D3, ffmpeg-wasi#60.
//
// Some job-spec options make a component iterate over candidate paths. Today
// each candidate is a host `access()` and costs about a microsecond. Under spec
// 0043 D1 every one becomes a bridge round trip, measured at 110µs, so an option
// the caller can set to `INT_MAX` stops being merely wasteful and becomes a job
// that produces no output and never ends — with nothing for a containment test
// to catch, because nothing escapes.
//
// The surface is two options, and it is small for a structural reason:
// `avio_check` has three call sites in all of libavformat and libavfilter, and
// all three are in `img2dec.c`. Everything else that opens paths on its own
// initiative is a network protocol, and the build passes `--disable-network`.
//
//	start_number_range   image2   default 5    range [1, INT_MAX]   flat scan
//	recursion_depth      concat   default 10   range [0, INT_MAX]   nested playlists
//
// # Why this refuses rather than clamps
//
// A clamp changes what the job means without saying so: the caller asked to scan
// a range and silently got a different one. That is the defect #54 was raised
// for, in a different costume.
//
// # Why this asserts a refusal and not a duration
//
// Asserting "the job completes quickly" would pass on a fast host and fail on a
// loaded one — a flake rather than a check. The ceiling is a property of the
// spec parser, so it is tested where it is enforced.

// optionCeiling is the largest iteration count any bounded option may request.
// Three orders of magnitude above observed need: a realistic 500-image sequence
// takes about 43 probes, because find_image_range gallops rather than scanning.
// At 110µs a round trip this bounds the worst case to roughly 1.1 seconds,
// against roughly 66 hours for the INT_MAX the options permit today.
const optionCeiling = 10000

func TestAnIterationOptionAboveTheCeilingIsRefused(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)
			in := writeClip(t, ws, "bo", 2, 0)

			job := func(opt string, v int) string {
				return fmt.Sprintf(
					`{"op":"process","inputs":[{"path":%q,"format":"image2",`+
						`"options":{"framerate":"25",%q:"%d"}}],"filter":"[0:v]null[v]",`+
						`"outputs":[{"path":%q,"map":["[v]"],"video_codec":"png"}]}`,
					in, opt, v, ws.Path("bo_%03d.png"))
			}

			// Refused above the ceiling, and the message names the option — a
			// diagnostic that does not say which key is at fault leaves the caller
			// guessing at a spec they wrote themselves.
			spec := job("start_number_range", optionCeiling+1)
			res := runAndCheck(t, ws.Runner(), t.Context(), spec, "start_number_range above the ceiling")
			if res.ExitCode == 0 {
				t.Errorf("%s: start_number_range=%d was accepted and the job exited 0 "+
					"(ffmpeg-wasi#60). Under spec 0043 D1 that is %d bridge round trips.\n"+
					"spec: %s", a, optionCeiling+1, optionCeiling+1, spec)
			} else if !strings.Contains(res.Stderr, "start_number_range") {
				t.Errorf("%s: refused, but stderr does not name the option: %q",
					a, strings.TrimSpace(res.Stderr))
			}

			// And accepted at the ceiling. Without this the test passes against an
			// engine that refuses the option outright, which would be a capability
			// regression wearing the same exit code.
			ok := job("start_number_range", optionCeiling)
			if res := runAndCheck(t, ws.Runner(), t.Context(), ok, "start_number_range at the ceiling"); res.ExitCode != 0 {
				t.Errorf("%s: start_number_range=%d was refused, but the ceiling is inclusive — "+
					"an engine that refuses the option entirely would pass the check above for "+
					"the wrong reason.\nstderr: %s", a, optionCeiling, strings.TrimSpace(res.Stderr))
			}
		})
	}
}
