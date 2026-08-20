package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
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

// TestAFinishedSinkDoesNotHoardFrames is the regression test for the defect the
// #19 fix introduced, found in review before it shipped.
//
// Marking a sink done and then SKIPPING it on every later pass is the obvious
// implementation, and it is wrong: a buffersink nobody reads still accumulates
// whatever the graph pushes to it. With two outputs sharing one graph and only
// one of them windowed, the finished sink queues every remaining frame of the
// source. Measured before the fix, on a 20-second source with a one-second
// window: peak RSS 14MB against 102MB. That grows with the source, so on a long
// input it is unbounded.
//
// # Why this asserts memory rather than output
//
// Both outputs were CORRECT throughout — 1.000s and 20.000s, exactly as asked.
// No assertion about the files could have caught this, which is precisely why it
// needed measuring instead. Peak RSS is the quantity that was wrong.
//
// # Why it is native-only
//
// The WASM engine runs inside the test binary, so its memory is not separable
// from Go's. The defect is in shared src/, so the native measurement covers it.
func TestAFinishedSinkDoesNotHoardFrames(t *testing.T) {
	// Long enough that hoarding is unmistakable against the baseline, short
	// enough to stay quick: 15s at 25fps of 320x320 is ~375 frames.
	const (
		seconds = 15
		rate    = 25
		side    = 320
	)

	// What hoarding would COST, in KiB, derived from the fixture rather than
	// guessed. The finished sink would hold every frame after its window: that
	// many yuv420p frames of this geometry. A relative multiplier was the first
	// attempt and it is the wrong instrument — it moves with the control
	// baseline and with the fixture length, so it can be satisfied by a fixture
	// too short to hoard much, which is the "check looser than the fault" shape
	// this file exists to avoid.
	const (
		hoardedFrames = (seconds - 1) * rate
		frameKiB      = side * side * 3 / 2 / 1024 // yuv420p
		hoardCostKiB  = hoardedFrames * frameKiB
		// Half of it is unmistakable: the fix should show ~0, the defect ~all of it.
		allowedKiB = hoardCostKiB / 2
	)

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)
			for i := range seconds * rate {
				png, err := fixture.PNG(side, side, i)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ws.Write(fmt.Sprintf("f_%04d.png", i), png); err != nil {
					t.Fatal(err)
				}
			}
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": ws.Path("f_%04d.png"), "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(rate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("hoard_src.mkv"), "map": []any{"[v]"},
					"video_codec": "libopenh264",
				}},
				"filter": "[0:v]null[v]",
			})

			// Two outputs off one graph. In the control both run to the end, so
			// neither sink is ever finished early; in the test one is windowed to
			// a second, so its sink finishes with ~14 seconds still to come.
			split := func(name string, window any) int64 {
				out := []any{
					map[string]any{"path": ws.Path(name + "_a.mkv"), "map": []any{"[a]"},
						"video_codec": "libopenh264"},
					map[string]any{"path": ws.Path(name + "_b.mkv"), "map": []any{"[b]"},
						"video_codec": "libopenh264"},
				}
				if window != nil {
					out[0].(map[string]any)["duration"] = window
				}
				spec, err := json.Marshal(map[string]any{
					"op":      "process",
					"inputs":  []any{map[string]any{"path": ws.Path("hoard_src.mkv")}},
					"filter":  "[0:v]split=2[a][b]",
					"outputs": out,
				})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
				defer cancel()
				res, err := ws.Runner().Run(ctx, string(spec))
				if err != nil {
					t.Fatalf("%s: %s: %v", a, name, err)
				}
				if res.ExitCode != 0 {
					t.Fatalf("%s: %s exited %d\n%s", a, name, res.ExitCode,
						strings.TrimSpace(res.Stderr))
				}
				if res.PeakRSSKiB == 0 {
					// Not a skip. A skip here is a vacuous pass, which is the exact
					// failure mode this suite exists to prevent — and the native
					// runner on Linux always reports this, so a zero means the
					// measurement broke, not that the platform cannot do it.
					t.Fatalf("%s: %s: the runner reported no peak RSS, so this test measured "+
						"nothing", a, name)
				}
				return res.PeakRSSKiB
			}

			control := split("ctl", nil)
			windowed := split("win", 1)

			// Low memory is only good news if the work still happened. A change that
			// bounded memory by truncating the shared graph — stopping BOTH outputs
			// when the windowed one finished — would pass a memory assertion alone.
			// So the unwindowed output must still be full length, and the windowed
			// one must still be its window.
			wantFrames(t, ws, a, "win_b.mkv", seconds*rate)
			wantFrames(t, ws, a, "win_a.mkv", 1*rate)

			// The control says what this job costs when nothing finishes early, so
			// the comparison is against measured baseline plus the arithmetic cost
			// of the defect — not against a multiplier that would drift.
			if grew := windowed - control; grew > allowedKiB {
				t.Errorf("%s: windowing one of two outputs grew peak memory by %d KiB "+
					"(%d KiB -> %d KiB). Hoarding every frame after the window would cost about "+
					"%d KiB (%d frames of %dx%d yuv420p), and this is over half of that — a "+
					"finished sink is holding the frames the graph keeps pushing to it.\n"+
					"Both output files are still correct, so only a memory measurement can see "+
					"this. The fix is to drain the done sink with AV_BUFFERSINK_FLAG_NO_REQUEST "+
					"and discard, never to skip it (ffmpeg-wasi#19).",
					a, grew, control, windowed, hoardCostKiB, hoardedFrames, side, side)
			}
		})
	}
}
