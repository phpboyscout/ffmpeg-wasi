package conformance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// Subtitle lane selection and cue windows — ffmpeg-wasi#21 and #22.
//
// # Why the fixture is SubRip written by hand
//
// It is three lines of text with the timings spelled out in it, so what went in
// is readable in the test source and what came out is readable in the assertion.
// No engine produced it, and no engine is asked what it contains — the output is
// SubRip too, and the assertion greps it. Spec 0036's rule against
// engine-produced fixtures usually costs something; here it is free.
//
// The cue timings are chosen so that each defect has a number attached to it. The
// long cue runs 0.5s→9.5s. Cut the output at 5s and it must end at 5s, not 9.5s.
// Seek the input to 3s and it must be 6.5s long, not 9s — the 2.5s that was
// discarded off its front is exactly the error the engine used to make.

const subFixture = `1
00:00:00,500 --> 00:00:09,500
the long cue

2
00:00:12,000 --> 00:00:13,000
well past the window

`

// subtitleArtifacts returns the artifacts that can demux SubRip and encode a
// subtitle format, skipping the rest with a reason. The lean profile carries no
// subtitle codecs at all, so it cannot exercise either defect.
func subtitleArtifacts(t *testing.T, a engine.Artifact, wantEncoder string) bool {
	t.Helper()

	caps, err := engine.Query(context.Background(), a.Runner())
	if err != nil {
		t.Fatalf("%s: querying capabilities: %v", a, err)
	}
	if !slices.Contains(caps.Demuxers, "srt") || !slices.Contains(caps.Decoders, "subrip") {
		t.Skipf("%s cannot read SubRip, so it cannot carry a subtitle stream at all", a)
	}
	if !slices.Contains(caps.Encoders, wantEncoder) {
		t.Skipf("%s carries no %s encoder", a, wantEncoder)
	}
	return true
}

// subWorkspace puts the SubRip fixture in a fresh workspace.
func subWorkspace(t *testing.T, a engine.Artifact) (*engine.Workspace, string) {
	t.Helper()
	ws := workspaceFor(t, a)
	in, err := ws.Write("in.srt", []byte(subFixture))
	if err != nil {
		t.Fatal(err)
	}
	return ws, in
}

// TestAnAbsoluteSubtitleMapStillTranscodes is the regression test for #21.
//
// `"0:s"` and `"0:0"` name the same stream in this fixture. The first works. The
// second parses to media type UNKNOWN — an absolute index carries no type, and
// cannot until the stream is resolved — and the engine chose the lane from that
// parsed type rather than from the stream it found. So `"0:0"` took the copy lane
// and `subtitle_codec` was dropped without a word.
//
// It fails loudly here rather than silently only because SubRip packets happen to
// be inadmissible to a WebVTT muxer. Into a container that accepted both it would
// have produced the wrong file and exit 0.
func TestAnAbsoluteSubtitleMapStillTranscodes(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			subtitleArtifacts(t, a, "webvtt")

			ws, in := subWorkspace(t, a)

			// Control: the by-type map must transcode. Without this, a failure
			// below could be the subtitle lane not working at all.
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": in}},
				"outputs": []any{map[string]any{
					"path": ws.Path("by-type.vtt"), "map": []any{"0:s"}, "subtitle_codec": "webvtt",
				}},
			})

			// The same job, naming the same stream by its absolute index.
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": in}},
				"outputs": []any{map[string]any{
					"path": ws.Path("absolute.vtt"), "map": []any{"0:0"}, "subtitle_codec": "webvtt",
				}},
			})

			// Both must be real WebVTT, not SubRip that was copied through.
			for _, name := range []string{"by-type.vtt", "absolute.vtt"} {
				body, err := ws.Read(name)
				if err != nil {
					t.Fatalf("%s: reading %s: %v", a, name, err)
				}
				if !strings.HasPrefix(string(body), "WEBVTT") {
					t.Errorf("%s: %s does not start with the WEBVTT signature — the stream was "+
						"copied, not transcoded (ffmpeg-wasi#21).\n%s", a, name, string(body))
				}
			}
		})
	}
}

// TestASubtitleCueIsClippedToItsWindow is the regression test for #22.
//
// Two halves, and the engine got both wrong in the same way: it decided WHETHER a
// cue belonged in the output but never adjusted HOW LONG it lasted.
func TestASubtitleCueIsClippedToItsWindow(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			subtitleArtifacts(t, a, "srt")

			// The tail: a 5s output, and a cue that runs to 9.5s.
			t.Run("at the output cutoff", func(t *testing.T) {
				ws, in := subWorkspace(t, a)
				runJob(t, ws, a, map[string]any{
					"op":     "process",
					"inputs": []any{map[string]any{"path": in}},
					"outputs": []any{map[string]any{
						"path": ws.Path("cut.srt"), "map": []any{"0:s"},
						"subtitle_codec": "srt", "duration": 5,
					}},
				})

				got := readCues(t, ws, a, "cut.srt")
				if strings.Contains(got, "00:00:09,500") {
					t.Errorf("%s: a cue in a 5-second output still ends at 9.5s "+
						"(ffmpeg-wasi#22) — it is on screen for 4.5s after the file ends.\n%s", a, got)
				}
				if !strings.Contains(got, "00:00:05,000") {
					t.Errorf("%s: the straddling cue was not clipped to the 5s cutoff "+
						"(ffmpeg-wasi#22).\n%s", a, got)
				}
			})

			// The head: seek to 3s, and a cue that started at 0.5s.
			t.Run("at the seek start", func(t *testing.T) {
				ws, in := subWorkspace(t, a)
				runJob(t, ws, a, map[string]any{
					"op":     "process",
					"inputs": []any{map[string]any{"path": in, "seek": map[string]any{"start": 3}}},
					"outputs": []any{map[string]any{
						"path": ws.Path("seek.srt"), "map": []any{"0:s"}, "subtitle_codec": "srt",
					}},
				})

				got := readCues(t, ws, a, "seek.srt")
				// 0.5s→9.5s seen from 3s is 6.5s long. The engine moved the start
				// to zero and kept the full 9s, so the cue outlasted its own text
				// by exactly the 2.5s that was discarded.
				if strings.Contains(got, "00:00:00,000 --> 00:00:09,000") {
					t.Errorf("%s: a cue clipped at the front kept its whole duration "+
						"(ffmpeg-wasi#22) — moved to 0 but still 9s long, when only 6.5s of it "+
						"is inside the output.\n%s", a, got)
				}
				if !strings.Contains(got, "00:00:06,500") {
					t.Errorf("%s: the seeked cue is not 6.5s long (ffmpeg-wasi#22).\n%s", a, got)
				}
			})
		})
	}
}

// readCues returns a SubRip output as text, for assertions that read like the
// file does.
func readCues(t *testing.T, ws *engine.Workspace, a engine.Artifact, name string) string {
	t.Helper()
	body, err := ws.Read(name)
	if err != nil {
		t.Fatalf("%s: reading %s: %v", a, name, err)
	}
	return string(body)
}

// TestATranscodedSubtitleKeepsItsMetadata is the regression test for #40.
//
// apply_output_metadata walked graph outputs and copied streams and stopped.
// The subtitle TRANSCODE lane (spec 0019) is neither, so `stream_metadata` was
// silently dropped for it — while a COPIED subtitle got it, because that rides
// the Cpy path.
//
// The same job spec therefore behaved differently depending on whether
// subtitle_codec was "copy" or a real encoder, which is the split #21 was about.
// The copy case is the control here for exactly that reason: it proves the
// routing works and isolates the missing lane.
func TestATranscodedSubtitleKeepsItsMetadata(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			subtitleArtifacts(t, a, "srt")

			language := func(name, codec string) string {
				ws, in := subWorkspace(t, a)
				runJob(t, ws, a, map[string]any{
					"op":     "process",
					"inputs": []any{map[string]any{"path": in}},
					"outputs": []any{map[string]any{
						"path": ws.Path(name), "map": []any{"0:s"}, "subtitle_codec": codec,
						"stream_metadata": map[string]any{
							"0:s": map[string]any{"language": "deu"},
						},
					}},
				})
				got := probe(t, ws, a, ws.Path(name)).Inputs[0]
				if len(got.Streams) != 1 {
					t.Fatalf("%s: %s has %d streams, want 1", a, name, len(got.Streams))
				}
				return got.Streams[0].Language
			}

			// Matroska, not SubRip: a .srt file is bare text with nowhere to put a
			// language, so both halves would report "" and the test would fail for
			// a reason that has nothing to do with the defect.
			//
			// Control: a COPIED subtitle carries it, so the routing itself works.
			if lang := language("copied.mkv", "copy"); lang != "deu" {
				t.Fatalf("%s: a copied subtitle stream lost its language (%q) — the metadata "+
					"routing is broken before the transcode lane is reached", a, lang)
			}

			if lang := language("encoded.mkv", "srt"); lang != "deu" {
				t.Errorf("%s: a TRANSCODED subtitle stream reports language %q, want \"deu\" "+
					"(ffmpeg-wasi#40).\nThe copy of the same job keeps it, so the request is "+
					"understood — the transcode lane was simply never visited by "+
					"apply_output_metadata.", a, lang)
			}
		})
	}
}
