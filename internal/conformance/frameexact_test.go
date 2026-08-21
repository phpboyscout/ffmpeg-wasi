package conformance

import (
	"fmt"
	"math"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// Frame-exact assertions — ffmpeg-wasi#11 and #12.
//
// # Counts and durations measure different failures
//
// The behavioural suite asserts duration against fixture arithmetic within
// durationTolerance, and #12 hid under it for the entire life of the project:
// one frame at 25fps is 0.04s against a 0.2s window. The check ran, passed, and
// could not have failed.
//
// Counting frames is the answer to that, and it works: measured against the
// released n9.0.1-1 driver, a 25-image passthrough into MP4 yields 24 frames
// where the fixed build and the ffmpeg CLI both yield 25. The ticket's own
// fixture agrees — 5/25/125 source frames give 4/24/124.
//
// #12 shows a SECOND symptom that a count cannot see. On a graph whose output
// does not end on a frame boundary — an xfade — the released engine emits the
// full 48 frames and the container still reports 1.566667s against 1.600000s.
// Same defect, same fix, and only a duration can observe that half of it.
//
// So this file holds both instruments, and neither is redundant. The counts
// guard #11 and the passthrough path; the duration assertion at the bottom
// guards the tail that survives a correct count, with a tolerance of half a
// frame rather than five.
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

// TestEveryFrameSurvivesAPassthrough is the frame-count guard for #12.
//
// A passthrough graph must emit as many frames as it was given. The released
// n9.0.1-1 driver does not: this fixture's 25 images come back as 24, and the
// ticket's own testsrc2 fixture gives 4/24/124 against 5/25/125.
//
// # A correction, because the wrong version of this comment was committed
//
// An earlier revision of this file asserted the opposite — that #12 was not a
// dropped frame, that this test passed against the engine it was written to
// catch, and that only a duration could see the defect. It cited a measurement
// showing 25 frames from the released engine.
//
// That measurement was taken against an artefact directory that did not hold the
// released driver for the variant under test, so the "released" column was the
// FIXED build compared with itself. Running this test against a genuinely
// released artefact fails, 24 against 25. The same trap — a stale or misidentified
// artefact reading exactly like a valid one — cost this suite a false baseline
// twice in one day, which is why it is written down here rather than quietly
// repaired.
//
// # What the duration test at the bottom is for, then
//
// Not a replacement for this one. #12 has a second symptom that a correct count
// cannot see: on an xfade, whose output does not end on a frame boundary, the
// released engine emits all 48 frames and the container still reports 1.566667s
// against 1.600000s. Both symptoms, one fix. Keep both assertions.
func TestEveryFrameSurvivesAPassthrough(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := workspaceFor(t, a)
			in := writeSequence(t, ws, seqFrames)

			// First: the container that is known good, so a failure below cannot be
			// blamed on the fixture or the graph.
			if got := countFrames(t, ws, a, in, "[0:v]null[v]"); got != seqFrames {
				t.Fatalf("%s: an image-sequence output emitted %d frames from %d inputs — "+
					"the fixture or the graph is wrong, before MP4 is even involved", a, got, seqFrames)
			}

			// Then the one that loses a frame (#12).
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": in, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(seqRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("out.mp4"), "map": []any{"[v]"}, "video_codec": "libopenh264",
				}},
				"filter": "[0:v]null[v]",
			})

			// Count by re-encoding the MP4 back out to an image sequence: one file
			// per decoded frame, so the count is independent of any duration the
			// muxer wrote.
			ws2 := workspaceFor(t, a)
			body, err := ws.Read("out.mp4")
			if err != nil {
				t.Fatalf("%s: reading the MP4: %v", a, err)
			}
			if _, err := ws2.Write("in.mp4", body); err != nil {
				t.Fatal(err)
			}

			runJob(t, ws2, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws2.Path("in.mp4")}},
				"outputs": []any{map[string]any{
					"path": ws2.Path("d_%03d.png"), "map": []any{"[v]"}, "video_codec": "png",
				}},
				"filter": "[0:v]null[v]",
			})

			out, err := ws2.Glob("d_*.png")
			if err != nil {
				t.Fatalf("%s: counting decoded frames: %v", a, err)
			}

			if len(out) != seqFrames {
				t.Errorf("%s: MP4 output holds %d frames, want %d (ffmpeg-wasi#12).\n"+
					"The same graph and encoder into an image sequence kept all %d, so this is the "+
					"MP4 finalisation path rather than the encoder. The ffmpeg CLI keeps all %d.",
					a, len(out), seqFrames, seqFrames, seqFrames)
			}
		})
	}
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

// #12's second symptom — a transition whose output length is not a whole number
// of frame intervals.
//
// # Why this fixture as well as the one above
//
// TestEveryFrameSurvivesAPassthrough catches the dropped frame, and on that
// fixture the count and the duration move together: lose the frame, lose 0.04s.
// An xfade separates them. Its output is two clips overlapped, so it does not end
// on a frame boundary, and there the released engine emits every frame and still
// reports a container 1/30s short. A count is blind to that; this is not a
// duplicate assertion.
//
// # Why the expected duration is arithmetic and not an oracle
//
// xfadeFrames frames at xfadeRate is one second per input by construction. An
// xfade starting at xfadeOffset and lasting xfadeTransition emits
// offset + transition + (clip - transition) seconds — the head of A, the overlap,
// then the tail of B. That is a number this test derives from its own fixture, so
// no external ffmpeg and no engine reply establishes it. Confirmed against the
// n9.0.1 CLI running the identical graph, which agrees to six decimal places.
//
// # Why the tolerance is half a frame
//
// The behavioural suite's durationTolerance is 0.2s. The defect is one frame —
// 0.033s at this rate — so it fits inside that window six times over, which is
// precisely how it survived every release. Half a frame is the widest tolerance
// that can still separate "correct" from "one frame short", and the failure it
// catches is exactly one frame, not a rounding wobble near the limit.
//
// Measured against the released engine (n9.0.1-1) and the fixed build, both
// encoders, native intermediate/gpl:
//
//	released   1.566667s   one frame short   FAIL
//	fixed      1.600000s   exact             PASS
const (
	xfadeRate       = 30  // frames per second for both clips
	xfadeFrames     = 30  // frames per clip — one second each
	xfadeTransition = 0.4 // seconds of overlap
	xfadeOffset     = 0.6 // seconds into the output where the overlap begins
)

// xfadeWantSec is the output length the fixture arithmetic demands.
const xfadeWantSec = xfadeOffset + xfadeTransition +
	(float64(xfadeFrames)/float64(xfadeRate) - xfadeTransition)

// halfFrameSec is half a frame interval at xfadeRate.
const halfFrameSec = 0.5 / float64(xfadeRate)

// writeClip puts n distinct PNGs into ws under the given prefix and returns the
// pattern a job spec should name them by. The seed offset keeps the two clips
// visually different, so the transition has something to transition between.
func writeClip(t *testing.T, ws *engine.Workspace, prefix string, n, seedBase int) string {
	t.Helper()

	for i := range n {
		png, err := fixture.PNG(64, 48, seedBase+i)
		if err != nil {
			t.Fatalf("building %s PNG %d: %v", prefix, i, err)
		}
		if _, err := ws.Write(fmt.Sprintf("%s_%02d.png", prefix, i), png); err != nil {
			t.Fatalf("writing %s PNG %d: %v", prefix, i, err)
		}
	}

	return ws.Path(prefix + "_%02d.png")
}

func TestAContainerReportsItsFullDurationAcrossATransition(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)
			clipA := writeClip(t, ws, "xa", xfadeFrames, 0)
			clipB := writeClip(t, ws, "xb", xfadeFrames, 100)

			in := func(pattern string) map[string]any {
				return map[string]any{
					"path": pattern, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(xfadeRate)},
				}
			}
			graph := fmt.Sprintf(
				"[0:v]format=yuv420p[a];[1:v]format=yuv420p[b];"+
					"[a][b]xfade=transition=fade:duration=%g:offset=%g[v]",
				xfadeTransition, xfadeOffset)

			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{in(clipA), in(clipB)},
				"outputs": []any{map[string]any{
					"path": ws.Path("xfade.mp4"), "map": []any{"[v]"},
					"video_codec": "libopenh264",
				}},
				"filter": graph,
			})

			// Control: the frames are all there. Without this, a failure below could
			// be the graph emitting too little rather than the container understating
			// what it holds — and those want different fixes.
			got := countFrames(t, ws, a, clipA, "[0:v]null[v]")
			if got != xfadeFrames {
				t.Fatalf("%s: the source clip yields %d frames, not %d — the fixture is "+
					"wrong before the transition is reached", a, got, xfadeFrames)
			}

			d := probe(t, ws, a, ws.Path("xfade.mp4")).Inputs[0].DurationSec
			if math.Abs(d-xfadeWantSec) > halfFrameSec {
				t.Errorf("%s: the transition output reports %.6fs, want %.6fs ±%.6f "+
					"(ffmpeg-wasi#12).\n"+
					"That is %.2f frames at %dfps. Two %d-frame clips at %dfps overlapped by "+
					"%gs starting at %gs is %.6fs by construction, and the n9.0.1 CLI running "+
					"this graph agrees. A short final sample duration makes the container "+
					"understate its own length by one frame; every frame is still present, "+
					"which is why a frame count cannot see this.",
					a, d, xfadeWantSec, halfFrameSec,
					math.Abs(d-xfadeWantSec)*xfadeRate, xfadeRate,
					xfadeFrames, xfadeRate, xfadeTransition, xfadeOffset, xfadeWantSec)
			}
		})
	}
}
