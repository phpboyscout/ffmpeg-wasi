package conformance

import (
	"fmt"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// A bounded output must bound the work — ffmpeg-wasi#19.
//
// The output window (`duration` / `end`) used to be enforced in one place only:
// drain_encoder threw away packets whose timestamps had passed the cutoff. That
// works for a graph fed by a finite input, because the input runs out and the
// job ends. It does not work for a graph that produces frames of its own accord.
//
// `loop=loop=-1` is the ordinary way to animate a still image, and it keeps
// emitting after its input is exhausted. Every frame was encoded and then
// discarded, forever — a job with a one-second answer that never returned it.
//
// # Why this test asserts a completed job and not a timing
//
// A hang has no natural assertion: any deadline is arbitrary, and the number that
// would make it flaky on a loaded machine is the same number that makes it useful.
// So the deadline is runJob's own, and it is generous. What makes the test sound
// is the CONTROL below: the same graph without `loop` must finish quickly, which
// distinguishes "the engine never terminates" from "this machine is slow".

// TestABoundedOutputBoundsAnEndlessGraph is the regression test for #19.
func TestABoundedOutputBoundsAnEndlessGraph(t *testing.T) {
	const windowSeconds = 1

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)
			// A single still image: the input is exhausted almost immediately, so
			// everything after the first frame comes from the filter.
			in := writeSequence(t, ws, 1)

			// Control: the same input and encoder with a finite graph. If this is
			// slow or broken, the assertion below means nothing.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": in, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(seqRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("finite.mp4"), "map": []any{"[v]"},
					"video_codec": "libopenh264", "duration": windowSeconds,
				}},
				"filter": "[0:v]null[v]",
			})

			// The same job through a filter that never ends. runJob fails on the
			// timeout with the message about a HANG, which is exactly the symptom.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": in, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(seqRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("looped.mp4"), "map": []any{"[v]"},
					"video_codec": "libopenh264", "duration": windowSeconds,
				}},
				"filter": fmt.Sprintf("[0:v]loop=loop=-1:size=1,setpts=N/%d/TB[v]", seqRate),
			})

			// And it must have produced the window that was asked for, rather than
			// terminating early with nothing in it — a stopped job is only a fix if
			// the answer is still there.
			wantFrames(t, ws, a, "looped.mp4", windowSeconds*seqRate)
		})
	}
}

// wantFrames decodes an output back to an image sequence and asserts the count,
// which is the quantity a windowing bug is actually wrong about. Durations carry
// muxer rounding; a file count does not.
func wantFrames(t *testing.T, ws *engine.Workspace, a engine.Artifact, name string, want int) {
	t.Helper()

	body, err := ws.Read(name)
	if err != nil {
		t.Fatalf("%s: reading %s: %v", a, name, err)
	}

	ws2 := workspaceFor(t, a)
	if _, err := ws2.Write("win.mp4", body); err != nil {
		t.Fatal(err)
	}
	runJob(t, ws2, a, map[string]any{
		"op":     "process",
		"inputs": []any{map[string]any{"path": ws2.Path("win.mp4")}},
		"outputs": []any{map[string]any{
			"path": ws2.Path("wd_%03d.png"), "map": []any{"[v]"}, "video_codec": "png",
		}},
		"filter": "[0:v]null[v]",
	})

	got, err := ws2.Glob("wd_*.png")
	if err != nil {
		t.Fatalf("%s: counting frames in %s: %v", a, name, err)
	}
	if len(got) != want {
		t.Errorf("%s: %s holds %d frames, want %d (ffmpeg-wasi#19) — the window was "+
			"applied, but not to the right amount of material", a, name, len(got), want)
	}
}
