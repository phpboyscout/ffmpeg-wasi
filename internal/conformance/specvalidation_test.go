package conformance

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// What the engine does with a job spec it should not accept — ffmpeg-wasi#23 and
// #30.
//
// The job spec is untrusted input. Not in the sense that a caller is an attacker
// — afmpeg builds it — but in the sense that the engine is the last thing between
// a number in a JSON document and signed 64-bit arithmetic, and it is the only
// component in a position to say no.
//
// Both defects here are the same shape as everything else found this week: the
// engine had an answer for a question it was never asked. A `duration` of 1e400
// produced a file and exit 0. A `chapters` of "junk" silently selected input 0.
// Neither wrote a word to stderr.

// TestASpecsNumbersAreCheckedBeforeTheyAreUsed is the regression test for #23.
//
// cJSON parses numbers with strtod, so "1e400" arrives as a double infinity and
// "0:a:0junk" arrives as a map entry sscanf is happy to read the front of. Every
// case below was ACCEPTED by the engine before this fix — that is the defect, not
// the specific message each one now produces.
func TestASpecsNumbersAreCheckedBeforeTheyAreUsed(t *testing.T) {
	// These are rejected while the outputs are parsed, before any input is
	// opened, so they need no fixture and run on every artifact.
	specOnly := []struct{ name, spec, want string }{
		{
			"a duration that is not finite",
			`{"op":"process","inputs":[{"path":"a.wav"}],"outputs":[{"path":"o.mp4","audio_codec":"aac","duration":1e400}]}`,
			"`duration` on o.mp4 is not a usable number of seconds",
		},
		{
			"an end that is not finite",
			`{"op":"process","inputs":[{"path":"a.wav"}],"outputs":[{"path":"o.mp4","audio_codec":"aac","end":1e400}]}`,
			"`end` on o.mp4 is not a usable number of seconds",
		},
		{
			"a duration far past any real timeline",
			`{"op":"process","inputs":[{"path":"a.wav"}],"outputs":[{"path":"o.mp4","audio_codec":"aac","duration":1e300}]}`,
			"`duration` on o.mp4 is not a usable number of seconds",
		},
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			for _, c := range specOnly {
				res := invoke(t, a, c.spec)
				if res.ExitCode == 0 {
					t.Errorf("%s: %s: exited 0 (ffmpeg-wasi#23).\nThe value is multiplied by "+
						"AV_TIME_BASE and cast to int64_t downstream, which is undefined for this "+
						"input — and the engine produced a file and reported success.\nspec: %s",
						a, c.name, c.spec)
					continue
				}
				if !strings.Contains(res.Stderr, c.want) {
					t.Errorf("%s: %s: rejected, but stderr does not carry %q: %q",
						a, c.name, c.want, strings.TrimSpace(res.Stderr))
				}
			}

			// These need a real input, because the engine only reaches them once
			// the streams it is selecting from exist.
			ws := mediaWorkspace(t, a)
			in := ws.Path("in.wav")

			withInput := []struct{ name, spec, want string }{
				{
					"a map entry with trailing rubbish",
					fmt.Sprintf(`{"op":"process","inputs":[{"path":%q}],"outputs":[{"path":%q,"map":["0:a:0junk"],"audio_codec":"copy"}]}`,
						in, ws.Path("o.mkv")),
					"cannot parse map entry",
				},
				{
					"a chapters value that is not a number",
					fmt.Sprintf(`{"op":"process","inputs":[{"path":%q}],"filter":"[0:a]anull[a]","outputs":[{"path":%q,"map":["[a]"],"audio_codec":"aac","chapters":"junk"}]}`,
						in, ws.Path("o2.mkv")),
					`is "junk"`,
				},
				{
					"a seek start that is not finite",
					fmt.Sprintf(`{"op":"process","inputs":[{"path":%q,"seek":{"start":1e400}}],"filter":"[0:a]anull[a]","outputs":[{"path":%q,"map":["[a]"],"audio_codec":"aac"}]}`,
						in, ws.Path("o3.mkv")),
					"finite, non-negative `start`",
				},
			}

			for _, c := range withInput {
				res, err := ws.Runner().Run(t.Context(), c.spec)
				if err != nil {
					t.Fatalf("%s: %s: invoking: %v", a, c.name, err)
				}
				if res.ExitCode == 0 {
					t.Errorf("%s: %s: exited 0 (ffmpeg-wasi#23).\nAccepting this quietly is the "+
						"defect: atoi() and sscanf() both have an answer for input they cannot read, "+
						"and the caller is never told their spec was not the one that ran.\nspec: %s",
						a, c.name, c.spec)
					continue
				}
				if !strings.Contains(res.Stderr, c.want) {
					t.Errorf("%s: %s: rejected, but stderr does not carry %q: %q",
						a, c.name, c.want, strings.TrimSpace(res.Stderr))
				}
			}
		})
	}
}

// TestAnEncoderWithNowhereToRunIsRefusedByName is the regression test for #30.
//
// Encoding happens at a graph output pad, and a pad reaches a file only by being
// named — bracketed — in that output's `map`. So `{"map":["0:a"],"audio_codec":
// "libopus"}` asks for two incompatible things: the job-spec reference says an
// unbracketed entry is stream-copied, and libopus cannot be applied to a copy.
//
// The engine used to synthesise a passthrough pad anyway and then fail to place
// it:
//
//	graph output pad [a] is not mapped to any output
//
// which names `[a]` — a label the caller never wrote and cannot find anywhere in
// their own job spec. THE EXIT CODE WAS ALREADY CORRECT. What this test asserts
// is the part that was not: that the diagnostic describes what the caller did.
func TestAnEncoderWithNowhereToRunIsRefusedByName(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := mediaWorkspace(t, a)
			spec := fmt.Sprintf(
				`{"op":"process","inputs":[{"path":%q}],"outputs":[{"path":%q,"map":["0:a"],"audio_codec":"aac"}]}`,
				ws.Path("in.wav"), ws.Path("o.mkv"))

			res, err := ws.Runner().Run(t.Context(), spec)
			if err != nil {
				t.Fatalf("%s: invoking: %v", a, err)
			}
			if res.ExitCode == 0 {
				t.Fatalf("%s: the job was accepted; it asks for an encoder on a stream-copied map", a)
			}

			// The message must quote the caller's own words back at them.
			for _, want := range []string{"audio_codec", `"aac"`, "`map`", "stream-copied"} {
				if !strings.Contains(res.Stderr, want) {
					t.Errorf("%s: the rejection does not mention %s (ffmpeg-wasi#30).\n"+
						"stderr: %s", a, want, strings.TrimSpace(res.Stderr))
				}
			}
			// And it must NOT be the old one, which named an internal pad.
			if strings.Contains(res.Stderr, "is not mapped to any output") {
				t.Errorf("%s: still reporting an unmapped graph pad (ffmpeg-wasi#30).\n"+
					"That pad is one the engine synthesised; the caller cannot act on it.\n"+
					"stderr: %s", a, strings.TrimSpace(res.Stderr))
			}
		})
	}
}

// TestAnAbsentMuxerSaysWhatIsPresent is the regression test for #29.
//
// The ticket was filed as "the lean profile has no ogg muxer, and nothing says
// so". The second half turned out to be untrue — docs/reference/containers.md
// states the lean muxer set and calls the read/write asymmetry out in bold — so
// what is fixed here is narrower and, it turns out, worse than the reported
// problem: the DIAGNOSTIC sent the caller in the wrong direction.
//
// libavformat's advice on an unresolvable format is "use a standard extension for
// the filename or specify the format manually". Both suggestions are dead ends
// for these builds. `.ogg` IS a standard extension, and naming the muxer in
// `outputs[].format` cannot conjure one that was never compiled in — the build
// starts from --disable-everything. A caller following that advice tries two
// things that cannot work before suspecting the profile.
//
// So the engine now lists what it does carry, and this asserts that.
func TestAnAbsentMuxerSaysWhatIsPresent(t *testing.T) {
	// Formats that some profile lacks. The first one an artifact does NOT carry
	// is the one it gets asked for.
	candidates := []string{"ogg", "adts", "flac", "asf", "rm"}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}

			absent := ""
			for _, c := range candidates {
				if !slices.Contains(caps.Muxers, c) {
					absent = c
					break
				}
			}
			if absent == "" {
				t.Skipf("%s carries every candidate muxer, so it cannot exercise the message", a)
			}

			spec := fmt.Sprintf(
				`{"op":"process","inputs":[{"path":"a.wav"}],"outputs":[{"path":"o.%s","format":%q,"audio_codec":"aac"}]}`,
				absent, absent)
			res := invoke(t, a, spec)

			if res.ExitCode == 0 {
				t.Fatalf("%s: asking for the absent muxer %q succeeded", a, absent)
			}
			if !strings.Contains(res.Stderr, "this build carries these muxers") {
				t.Errorf("%s: asked for the absent muxer %q and was not told what is present "+
					"(ffmpeg-wasi#29).\nlibavformat's own advice — a standard extension, or naming "+
					"the format — cannot work for a muxer that is not compiled in, so the caller "+
					"needs the list to know that.\nstderr: %s", a, absent, strings.TrimSpace(res.Stderr))
			}
			// And the list must be real: a muxer this artifact reports having.
			if len(caps.Muxers) > 0 && !strings.Contains(res.Stderr, caps.Muxers[0]) {
				t.Errorf("%s: the muxer list does not mention %q, which --capabilities says is "+
					"present — the two disagree.\nstderr: %s",
					a, caps.Muxers[0], strings.TrimSpace(res.Stderr))
			}
		})
	}
}
