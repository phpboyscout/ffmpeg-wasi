package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
				res := runAndCheck(t, ws.Runner(), t.Context(), c.spec, c.name)
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

			res := runAndCheck(t, ws.Runner(), t.Context(), spec, "the job")
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
				// Not expected to happen — no build carries an `asf` or `rm` muxer —
				// but if the enable-list ever grows to cover every candidate, this
				// test would quietly stop testing anything. Fail rather than skip:
				// a vacuous pass is the failure mode this whole suite exists to avoid.
				t.Fatalf("%s carries every candidate muxer %v, so this test can no longer "+
					"exercise the message — add a candidate that is genuinely absent", a, candidates)
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
			// The list must be COMPLETE, not merely present. Checking one name would
			// pass against a diagnostic that printed a single muxer, or a stale
			// subset — and an incomplete list is worse than none, because it tells
			// the caller a container is unavailable when it is not.
			listed := res.Stderr
			if i := strings.Index(listed, "carries these muxers:"); i >= 0 {
				listed = listed[i:]
			}
			var missing []string
			for _, m := range caps.Muxers {
				if !strings.Contains(listed, m) {
					missing = append(missing, m)
				}
			}
			if len(missing) > 0 {
				t.Errorf("%s: the muxer list omits %d of the %d muxers --capabilities reports: %s\n"+
					"An incomplete list is worse than none — it tells the caller a container is "+
					"absent when the build has it.\nstderr: %s",
					a, len(missing), len(caps.Muxers), strings.Join(missing, ", "),
					strings.TrimSpace(res.Stderr))
			}
		})
	}
}

// TestAnOversizedFramesTemplateIsRefused is the regression test for #53.
//
// expand_path formatted into a fixed 1024-byte buffer and discarded snprintf's
// return. Truncation eats the TAIL, which is where the frame index lives — so
// every frame expanded to the same path, each overwrote the last, and the reply
// still listed them all. One file on disk, N in the answer.
//
// The assertion is deliberately not "the job fails": refusing is one legitimate
// fix and so is a larger buffer. What must not happen is a success whose reply
// claims more files than exist.
func TestAnOversizedFramesTemplateIsRefused(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := mediaWorkspace(t, a)
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": ws.Path("f%03d.png"), "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(frameRate)},
				}},
				"outputs": []any{map[string]any{"path": ws.Path("seq.mp4"), "video_codec": "mjpeg"}},
			})

			// The path has to be long in TOTAL while every component stays legal.
			// A single 1100-character filename does not work: the filesystem
			// refuses it at NAME_MAX long before the buffer matters, so the job
			// fails for the wrong reason and the truncation is never exercised.
			//
			// Nested directories get past 1024 bytes with nothing over 255, and
			// they must already EXIST — a truncated path pointing at a missing
			// directory would also fail for the wrong reason. ws.Write creates them.
			deep := ""
			for range 5 {
				deep += strings.Repeat("d", 200) + "/"
			}
			if _, err := ws.Write(deep+"keep.txt", []byte("makes the directories")); err != nil {
				t.Fatal(err)
			}
			// The tail is what truncation eats, and the index lives in the tail.
			tmpl := ws.Path(deep + strings.Repeat("n", 40) + "_%02d.png")
			if len(tmpl) <= 1024 {
				t.Fatalf("the template is only %d bytes, which cannot overflow the engine's "+
					"1024-byte buffer — this test would measure nothing", len(tmpl))
			}

			spec, err := json.Marshal(map[string]any{
				"op":     "frames",
				"inputs": []any{map[string]any{"path": ws.Path("seq.mp4")}},
				"select": map[string]any{"timestamps": []any{0.5, 1.5, 2.5}},
				"path":   tmpl,
				"codec":  "png",
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
			defer cancel()
			res := runAndCheck(t, ws.Runner(), ctx, string(spec), "the frames job")
			if res.ExitCode != 0 {
				return // refused, which is a legitimate answer
			}

			var reply struct {
				Frames []struct{} `json:"frames"`
				Count  int        `json:"count"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &reply); err != nil {
				t.Fatalf("%s: parsing the reply: %v\n%s", a, err, res.Stdout)
			}
			written, err := filepath.Glob(filepath.Join(filepath.Dir(tmpl), "*.png"))
			if err != nil {
				t.Fatal(err)
			}
			if reply.Count != len(written) {
				t.Errorf("%s: the job succeeded claiming %d frames, but %d file(s) exist "+
					"(ffmpeg-wasi#53).\nThe template overflowed a 1024-byte buffer and snprintf "+
					"truncated it — taking the index with it — so every frame expanded to the "+
					"same path and overwrote the last.", a, reply.Count, len(written))
			}
		})
	}
}

// TestASpecIsTakenLiterallyOrRefused covers #42, #49 and #50 — three ways the
// engine used to accept something it could not act on, and act as though it had.
//
// The common shape: a well-formed request that the engine partly ignored, then
// reported success for. None of them is a parse failure; each is the engine
// answering a question nobody asked.
func TestASpecIsTakenLiterallyOrRefused(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := mediaWorkspace(t, a)
			in := ws.Path("in.wav")

			cases := []struct{ name, spec, want string }{
				{
					// #42 — the natural way to write a numeric option. It was
					// dropped in silence, so a raw input decoded at libav's default
					// while the caller believed the option had been honoured.
					"a numeric demuxer option",
					fmt.Sprintf(`{"op":"process","inputs":[{"path":%q,"options":{"sample_rate":48000}}],`+
						`"filter":"[0:a]anull[a]","outputs":[{"path":%q,"map":["[a]"],"audio_codec":"flac"}]}`,
						in, ws.Path("o1.mkv")),
					"must be a string",
				},
				{
					"a numeric encoder option",
					fmt.Sprintf(`{"op":"process","inputs":[{"path":%q}],"filter":"[0:a]anull[a]",`+
						`"outputs":[{"path":%q,"map":["[a]"],"audio_codec":"flac","options":{"compression_level":5}}]}`,
						in, ws.Path("o2.mkv")),
					"must be a string",
				},
				{
					// #50 — a valid object with rubbish after it.
					"trailing text after a valid spec",
					fmt.Sprintf(`{"op":"probe","inputs":[{"path":%q}]} and then some`, in),
					"",
				},
				{
					// #49 — process refuses an unknown demuxer; probe auto-probed,
					// so the two ops disagreed about the same spec.
					"probe with an unknown forced format",
					fmt.Sprintf(`{"op":"probe","inputs":[{"path":%q,"format":"no-such-demuxer"}]}`, in),
					"unknown input format",
				},
				{
					// #49 — and this one SEGFAULTS the released engine. A missing
					// `path` became a NULL handed to avformat_open_input. Exit 139
					// is not zero, so a check spelled "the exit code is not zero"
					// is satisfied by the crash — the same trap that let #15 be
					// recorded as not-reproduced. The signal assertion below is
					// what makes this test mean anything.
					"probe with no path",
					`{"op":"probe","inputs":[{}]}`,
					"needs a string `path`",
				},
			}

			for _, c := range cases {
				// Deliberately NOT runAndCheck: the crash is the finding here, so it
				// has to be reported against #49 by name rather than as a generic
				// "killed" failure from the shared helper.
				res, err := ws.Runner().Run(t.Context(), c.spec)
				if err != nil {
					t.Fatalf("%s: %s: invoking: %v", a, c.name, err)
				}
				if res.Signal != "" {
					t.Errorf("%s: %s: the engine was KILLED BY %s rather than refusing the "+
						"request.\nA signalled process reports a non-zero code, so this would "+
						"otherwise read as a clean rejection.\nspec: %s",
						a, c.name, res.Signal, c.spec)
					continue
				}
				if res.ExitCode == 0 {
					t.Errorf("%s: %s: accepted and exited 0.\nThe engine acted on a request it "+
						"could not honour in full, and told the caller it had.\nspec: %s",
						a, c.name, c.spec)
					continue
				}
				if c.want != "" && !strings.Contains(res.Stderr, c.want) {
					t.Errorf("%s: %s: refused, but stderr does not carry %q: %q",
						a, c.name, c.want, strings.TrimSpace(res.Stderr))
				}
			}
		})
	}
}
