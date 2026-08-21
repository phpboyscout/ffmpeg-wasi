package conformance

import (
	"fmt"
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
