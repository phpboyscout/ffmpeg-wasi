package conformance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// An endless input with a graph-bounded length — ffmpeg-wasi#58.
//
// The demux loop's only exit was every input reaching EOF. An input that has no
// end — `loop=1` on a still image, which is how you build a title card or a hold
// — kept it spinning long after the graph had closed every pad it feeds, decoding
// packets that had nowhere to go. The job ran until something killed it.
//
// The n9.0.1 CLI given the identical graph terminates immediately, so this is the
// engine's loop rather than anything about the filter chain.
//
// # Why this is a test and not a timeout tuning exercise
//
// runJob fails a job that outruns jobTimeout, so the hang is caught by the
// harness. What the assertions add is the other half: that stopping early did not
// become stopping short. The frame count has to be right, and the companion test
// below has to keep its copied stream intact — a stop condition that ignored the
// copy lane would turn this hang into silent data loss, which is worse.

const (
	holdRate    = 30  // frames per second the still is held at
	holdSeconds = 1.0 // seconds the graph keeps
)

// stillInput is a single PNG the image2 demuxer is told to loop forever.
func stillInput(t *testing.T, ws *engine.Workspace, name string, seed int) map[string]any {
	t.Helper()

	png, err := fixture.PNG(64, 48, seed)
	if err != nil {
		t.Fatalf("building the still: %v", err)
	}
	if _, err := ws.Write(name, png); err != nil {
		t.Fatalf("writing the still: %v", err)
	}

	return map[string]any{
		"path": ws.Path(name), "format": "image2",
		"options": map[string]any{"framerate": fmt.Sprint(holdRate), "loop": "1"},
	}
}

func TestAGraphBoundedJobFinishesOnAnEndlessInput(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)

			// One still, looped forever, with the length set entirely by the trim.
			// Nothing but the graph bounds this job.
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{stillInput(t, ws, "card.png", 7)},
				"outputs": []any{map[string]any{
					"path": ws.Path("h_%03d.png"), "map": []any{"[v]"}, "video_codec": "png",
				}},
				"filter": fmt.Sprintf("[0:v]trim=duration=%g,setpts=PTS-STARTPTS[v]", holdSeconds),
			})

			// Terminating is necessary but not sufficient: an engine that stopped the
			// moment it saw a closed pad would also terminate, and would emit nothing.
			got, err := ws.Glob("h_*.png")
			if err != nil {
				t.Fatalf("%s: counting held frames: %v", a, err)
			}
			if want := int(holdSeconds * holdRate); len(got) != want {
				t.Errorf("%s: a %gs hold at %dfps emitted %d frames, want %d — the job "+
					"terminated but did not produce the length the graph asks for "+
					"(ffmpeg-wasi#58)", a, holdSeconds, holdRate, len(got), want)
			}
		})
	}
}

// TestStoppingAnEndlessInputDoesNotCutACopiedStreamShort is what #58 guards
// against its own fix.
//
// The stop condition cannot be "the graph is finished". An input can still owe
// packets to a passthrough output that never went through the graph, and an
// engine that stopped on the graph alone would truncate it — trading a hang for
// silent data loss, which is the worse of the two.
//
// So: one endless input bounded by the graph, and one finite input copied
// verbatim into a second output file. The copy must arrive whole.
//
// # Why this counts frames rather than asking for a duration
//
// The first version of this test asserted the copied stream's duration, and it
// failed against a copy that was demonstrably complete — all 40 frames present,
// the container reporting 1.300s instead of 1.333s. That is the copy lane
// dropping its final packet duration, which is real, predates this work, and is
// filed separately; it is not truncation. A duration cannot tell the two apart,
// so this counts frames, which can.
func TestStoppingAnEndlessInputDoesNotCutACopiedStreamShort(t *testing.T) {
	// Deliberately longer than the hold, so the copy is still running when the
	// endless input is stopped. If the two ended together the test would pass
	// without exercising anything.
	const copyFrames = 40

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)

			// A real finite stream to copy. Built through the engine, which is safe
			// here: what is asserted is that a copy of it survives intact, not
			// anything about its contents, so the engine cannot agree with its own
			// mistake the way spec 0036 warns about.
			seq := writeClip(t, ws, "src", copyFrames, 200)
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": seq, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(holdRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("copyme.mkv"), "map": []any{"[v]"},
					"video_codec": "libopenh264",
				}},
				"filter": "[0:v]null[v]",
			})

			// Input 0 is endless and bounded by the graph; input 1 is finite and
			// copied straight through to its own file. If the demux loop stops on the
			// graph alone, input 1 loses its tail.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{
					stillInput(t, ws, "hold.png", 9),
					map[string]any{"path": ws.Path("copyme.mkv")},
				},
				"outputs": []any{
					map[string]any{
						"path": ws.Path("hold.mkv"), "map": []any{"[v]"},
						"video_codec": "libopenh264",
					},
					map[string]any{
						"path": ws.Path("kept.mkv"), "map": []any{"1:v"},
						"video_codec": "copy",
					},
				},
				"filter": fmt.Sprintf("[0:v]trim=duration=%g,setpts=PTS-STARTPTS[v]", holdSeconds),
			})

			// Decode the copy back out, one file per frame, so the count is `ls`
			// rather than anything the engine reported about its own work.
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("kept.mkv")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("k_%03d.png"), "map": []any{"[v]"}, "video_codec": "png",
				}},
				"filter": "[0:v]null[v]",
			})

			kept, err := ws.Glob("k_*.png")
			if err != nil {
				t.Fatalf("%s: counting the copied frames: %v", a, err)
			}
			if len(kept) != copyFrames {
				t.Errorf("%s: the copied stream kept %d of %d frames — stopping the endless "+
					"input cut the passthrough lane short, which turns #58's hang into silent "+
					"truncation (ffmpeg-wasi#58)", a, len(kept), copyFrames)
			}
		})
	}
}

// TestAnOutputWindowEndsAJobOnAnEndlessInput is the corpus case where the sinks
// close on their own window rather than on a filter — afmpeg spec 0044 D2.
//
// This is #19's shape: an infinite filter tail defeats `outputs[].duration`, so
// the job can run forever. It is the first member of the liveness family, and
// #58 was the second — caused by #19's own fix being incomplete, because the
// sink flag it introduced meant only "the window is satisfied" and never "the
// sink reached EOF".
//
// Keeping both in one file is deliberate: the two stop conditions are different
// code paths that must both terminate, and a corpus with only one of them would
// have looked complete while #58 was live.
func TestAnOutputWindowEndsAJobOnAnEndlessInput(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)

			// No trim: nothing in the graph bounds this. Only the output window does.
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{stillInput(t, ws, "win.png", 11)},
				"outputs": []any{map[string]any{
					"path": ws.Path("w_%03d.png"), "map": []any{"[v]"},
					"video_codec": "png", "duration": holdSeconds,
				}},
				"filter": "[0:v]null[v]",
			})

			got, err := ws.Glob("w_*.png")
			if err != nil {
				t.Fatalf("%s: counting windowed frames: %v", a, err)
			}
			// The window is enforced on a timestamp, so the boundary frame may or may
			// not be included. What must not happen is a short or empty output — that
			// is a stop condition firing too early, which is the failure a naive fix
			// introduces.
			want := int(holdSeconds * holdRate)
			if got := len(got); got < want-1 || got > want+1 {
				t.Errorf("%s: a %gs output window on an endless input emitted %d frames, "+
					"want %d ±1 (ffmpeg-wasi#19, #60)", a, holdSeconds, got, want)
			}
		})
	}
}

// TestStoppingAnEndlessInputDoesNotCutASubtitleLaneShort is the third lane.
//
// D2 requires all three lanes to agree before an input is called spent. The copy
// lane has its own test above; this one covers the subtitle lane, which runs
// beside the graph rather than through it and so is invisible to any check based
// on graph pads alone.
func TestStoppingAnEndlessInputDoesNotCutASubtitleLaneShort(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			subtitleArtifacts(t, a, "srt")

			ws, srt := subWorkspace(t, a)

			// Input 0 is endless and bounded by the graph; input 1 carries the cues
			// and is finite. If the loop stops on the graph alone, the cues are lost.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{
					stillInput(t, ws, "subhold.png", 13),
					map[string]any{"path": srt},
				},
				"outputs": []any{
					map[string]any{
						"path": ws.Path("subhold.mkv"), "map": []any{"[v]"},
						"video_codec": "libopenh264",
					},
					map[string]any{
						"path": ws.Path("kept.srt"), "map": []any{"1:s"},
						"subtitle_codec": "srt",
					},
				},
				"filter": fmt.Sprintf("[0:v]trim=duration=%g,setpts=PTS-STARTPTS[v]", holdSeconds),
			})

			// The fixture's second cue starts at 12s, well past the 1s hold, so a lane
			// cut when the video stopped loses it. Counting cue timing lines rather
			// than probing: a probe asks the engine what it thinks it wrote, and that
			// is the account under test.
			got := strings.Count(readCues(t, ws, a, "kept.srt"), "-->")
			want := strings.Count(subFixture, "-->")
			if got != want {
				t.Errorf("%s: the subtitle lane kept %d of %d cues — the endless video "+
					"input stopping cut a lane that had its own work left (ffmpeg-wasi#60).\n"+
					"The fixture's last cue starts at 12s, well past the %gs hold.",
					a, got, want, holdSeconds)
			}
		})
	}
}

// TestStoppingAnInputSaysWhy is afmpeg spec 0044 D4.
//
// A job that ends because nothing can consume an input any more looks identical,
// from outside, to one that ended because the input ran out. When this family
// recurs — and it has recurred once already, #58 through #19's own incomplete fix
// — the caller gets a short output or a hang with nothing to read.
//
// So the engine says which input, and the state of all three lanes, because the
// bug is always that one lane was consulted and another was not.
//
// The diagnostic is worth reading on the fixture below: it reports one graph pad
// STILL OPEN while every graph output has finished. That is not a contradiction,
// it is #58's exact mechanism — libavfilter auto-inserts a format converter
// between the trim and the sink, and the EOF the trim raises is absorbed there
// and never reaches the source pad. A stop condition asking the sources would
// still be waiting.
func TestStoppingAnInputSaysWhy(t *testing.T) {
	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)
			spec, err := json.Marshal(map[string]any{
				"op":     "process",
				"inputs": []any{stillInput(t, ws, "why.png", 17)},
				"outputs": []any{map[string]any{
					"path": ws.Path("why_%03d.png"), "map": []any{"[v]"}, "video_codec": "png",
				}},
				"filter": fmt.Sprintf("[0:v]trim=duration=%g,setpts=PTS-STARTPTS[v]", holdSeconds),
			})
			if err != nil {
				t.Fatal(err)
			}

			res := runAndCheck(t, ws.Runner(), t.Context(), string(spec), "the endless-input job")
			if res.ExitCode != 0 {
				t.Fatalf("%s: the job failed (exit %d)\n%s", a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}

			// Naming the input is the point. "something finished early" is what the
			// caller already knows; which input, and which lanes were dead, is what
			// they cannot work out from the output.
			for _, want := range []string{"input 0", "no consumer left", "graph pad", "copy target", "subtitle target"} {
				if !strings.Contains(res.Stderr, want) {
					t.Errorf("%s: the stop diagnostic does not mention %q (ffmpeg-wasi#60).\n"+
						"stderr: %s", a, want, strings.TrimSpace(res.Stderr))
				}
			}
		})
	}
}
