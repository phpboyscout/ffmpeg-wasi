package conformance

import (
	"fmt"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// Frame-exact assertions — ffmpeg-wasi#11 and #12.
//
// # Why these do not assert durations
//
// The behavioural suite asserts duration against fixture arithmetic within a
// tolerance, and #12 hid under it for the entire life of the project: one frame
// at 25fps is 0.04s, comfortably inside the window at every fixture length. The
// check ran, passed, and could not have failed.
//
// So these count frames. A count is the quantity the engine is actually wrong
// about; a duration is a derived number with muxer rounding in it, which is what
// made it a poor instrument.
//
// # Why the fixture is a PNG sequence
//
// The expected answer has to be arithmetic this test owns rather than something
// the engine reported. N images written by internal/fixture, read back through
// the image2 demuxer at a known frame rate, means N frames went in — no engine
// was consulted to establish that. Spec 0036's rule against engine-produced
// fixtures exists for exactly this reason, and a duration fixture would have
// handed the engine the chance to agree with its own mistake.
//
// Counting the way out uses the same trick: encoding to an image2 sequence
// writes one file per frame, so the count is `ls`, not a probe.

const (
	seqFrames = 25 // images in the source sequence
	seqRate   = 25 // frames per second the image2 demuxer is told to assume
)

// workspaceFor is a bare workspace — the media fixtures mediaWorkspace writes are
// not wanted here, since this file supplies its own.
func workspaceFor(t *testing.T, a engine.Artifact) *engine.Workspace {
	t.Helper()
	ws, err := engine.NewWorkspace(t.TempDir(), a)
	if err != nil {
		t.Fatalf("%s: preparing the workspace: %v", a, err)
	}
	return ws
}

// writeSequence puts N distinct PNGs into ws as f_00.png … and returns the
// pattern a job spec should name them by.
func writeSequence(t *testing.T, ws *engine.Workspace, n int) string {
	t.Helper()

	for i := range n {
		// A distinct image per frame, so a dropped or duplicated frame is a real
		// difference rather than something a codec could paper over.
		png, err := fixture.PNG(64, 48, i)
		if err != nil {
			t.Fatalf("building PNG %d: %v", i, err)
		}
		if _, err := ws.Write(fmt.Sprintf("f_%02d.png", i), png); err != nil {
			t.Fatalf("writing PNG %d: %v", i, err)
		}
	}

	return ws.Path("f_%02d.png")
}

// countFrames encodes the graph's output as one PNG per frame and counts the
// files. It deliberately does not probe: a probe asks the engine how many frames
// it thinks it wrote, and the engine's own account is the thing under test.
func countFrames(t *testing.T, ws *engine.Workspace, a engine.Artifact, in, filter string) int {
	t.Helper()

	runJob(t, ws, a, map[string]any{
		"op": "process",
		"inputs": []any{map[string]any{
			"path": in, "format": "image2",
			"options": map[string]any{"framerate": fmt.Sprint(seqRate)},
		}},
		"outputs": []any{map[string]any{
			"path": ws.Path("out_%03d.png"), "map": []any{"[v]"}, "video_codec": "png",
		}},
		"filter": filter,
	})

	got, err := ws.Glob("out_*.png")
	if err != nil {
		t.Fatalf("%s: counting output frames: %v", a, err)
	}
	return len(got)
}

// TestAGraphMayFinishBeforeItsInput is the regression test for #11.
//
// A filter graph that completes while its input still has decoded frames — a
// trim, or a transition shorter than the clips — must finish the job, not fail
// it. The engine returned AVERROR_EOF from the closed buffersrc as a job error.
//
// Two details this test got wrong on the first attempt, both worth keeping:
//
//   - It must read a REAL DECODED STREAM. An image2 sequence does not reproduce
//     it: each still is demuxed on demand, so nothing is ever left queued when the
//     graph closes. That matches what the keryx session worked out independently —
//     a generated or single-frame source cannot strand input. So the fixture is
//     built into a video first.
//   - The symptom is the EXIT CODE, not the frame count. The engine emitted the
//     right ten frames and then failed the job. Asserting only the count passed
//     against the broken engine, which is the same "check that could not fail"
//     mistake #12 was hiding behind. runJob fails on a non-zero exit, which is
//     what makes this red.
//
// The intermediate is Matroska deliberately: MP4 drops a frame (#12), and a
// fixture carrying a second known bug would make this test's arithmetic wrong.
func TestAGraphMayFinishBeforeItsInput(t *testing.T) {
	const keep = 10 // frames the trim keeps, well short of seqFrames

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := workspaceFor(t, a)
			in := writeSequence(t, ws, seqFrames)

			// A real video stream, so the decoder has frames queued behind the graph.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": in, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(seqRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("mid.mkv"), "map": []any{"[v]"}, "video_codec": "libopenh264",
				}},
				"filter": "[0:v]null[v]",
			})

			// keep frames of a seqFrames-long source: seqFrames-keep decoded frames
			// are still pending when the graph closes. runJob fails the test if the
			// engine exits non-zero, which is the #11 symptom.
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("mid.mkv")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("t_%03d.png"), "map": []any{"[v]"}, "video_codec": "png",
				}},
				"filter": fmt.Sprintf("[0:v]trim=end_frame=%d,setpts=PTS-STARTPTS[v]", keep),
			})

			got, err := ws.Glob("t_*.png")
			if err != nil {
				t.Fatalf("%s: counting trimmed frames: %v", a, err)
			}
			if len(got) != keep {
				t.Errorf("%s: a graph that finishes before its input emitted %d frames, want %d "+
					"(ffmpeg-wasi#11)", a, len(got), keep)
			}
		})
	}
}
