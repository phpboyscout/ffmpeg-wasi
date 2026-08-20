package conformance

import (
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// Chapters and the output window — ffmpeg-wasi#41.
//
// copy_chapters copied start and end verbatim. Nothing rebased them against the
// input's seek and nothing clipped them to the output window, so seeking to 60s
// left a chapter at 70s sitting at 70s in an output whose media now starts at
// zero, and chapters past a duration cutoff pointed beyond the end of the file.
//
// Exactly #22's defect on a different carrier: the engine decided WHETHER to
// carry the item and never adjusted WHERE it sits.
//
// # The fixture is hand-built Matroska
//
// Nothing else here can produce a file with chapters — no ffmetadata demuxer in
// any profile, and asking the engine to make one means copying from a file that
// already has them. internal/fixture writes the EBML directly, so the timings
// asserted below are arithmetic this test owns (spec 0036).

func TestChaptersAreClippedToTheOutputWindow(t *testing.T) {
	// Chosen against a 30-second output window so each exercises an edge:
	//
	//   0–5s     wholly inside          -> kept unchanged
	//   20–40s   straddles the cutoff   -> clipped to 20–30s
	//   70–120s  wholly outside         -> dropped
	chapters := []fixture.Chapter{
		{Start: 0, End: 5, Title: "inside"},
		{Start: 20, End: 40, Title: "straddles"},
		{Start: 70, End: 120, Title: "outside"},
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := mediaWorkspace(t, a)
			if _, err := ws.Write("chapters.mkv", fixture.MatroskaWithChapters(chapters)); err != nil {
				t.Fatal(err)
			}

			copyChapters := func(name string, window any) []struct {
				Start float64 `json:"start"`
				End   float64 `json:"end"`
				Title string  `json:"title"`
			} {
				out := map[string]any{
					"path": ws.Path(name), "map": []any{"[a]"},
					"audio_codec": "flac", "chapters": "1",
				}
				if window != nil {
					out["duration"] = window
				}
				runJob(t, ws, a, map[string]any{
					"op": "process",
					"inputs": []any{
						map[string]any{"path": ws.Path("in.wav")},
						map[string]any{"path": ws.Path("chapters.mkv")},
					},
					"filter":  "[0:a]anull[a]",
					"outputs": []any{out},
				})
				return probe(t, ws, a, ws.Path(name)).Inputs[0].Chapters
			}

			// Control: unwindowed, all three arrive unchanged. Without it, "two
			// chapters" below could mean passthrough is broken rather than clipped.
			if all := copyChapters("all.mkv", nil); len(all) != 3 {
				t.Fatalf("%s: an unwindowed copy carried %d chapters, want 3 — chapter "+
					"passthrough is broken before any window is involved", a, len(all))
			}

			got := copyChapters("cut.mkv", 30)
			if len(got) != 2 {
				t.Errorf("%s: a 30-second output carries %d chapters, want 2 (ffmpeg-wasi#41).\n"+
					"The 70–120s chapter is entirely past the cutoff and should be gone.\n%+v",
					a, len(got), got)
				return
			}
			for i, want := range []struct{ start, end float64 }{{0, 5}, {20, 30}} {
				if c := got[i]; !near(c.Start, want.start) || !near(c.End, want.end) {
					t.Errorf("%s: chapter %d is %.3f–%.3fs, want %.3f–%.3fs (ffmpeg-wasi#41).\n"+
						"A chapter running to 40s in a 30-second output points past the end of "+
						"the file; it has to stop at the cutoff.",
						a, i, c.Start, c.End, want.start, want.end)
				}
			}
		})
	}
}

// near compares chapter times, which carry the source timebase's rounding.
func near(got, want float64) bool { return got-want < 0.05 && want-got < 0.05 }
