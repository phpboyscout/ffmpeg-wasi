package conformance

import (
	"fmt"
	"math"
	"testing"
)

// A copied stream's length — ffmpeg-wasi#59.
//
// #12 was the encode lane understating a container's length by one frame. The
// copy lane does the same thing for a different reason and was not fixed with
// it: the demuxer supplies no per-packet duration, Matroska carries the value in
// DefaultDuration which is written on encode but not carried across a copy, and
// the muxer is left with nothing to write for the final sample.
//
// Measured against the released n9.0.1-1 driver, a 40-frame 30fps source:
//
//	source                    1.333000s   40 frames
//	engine, copy              1.300000s   40 frames
//	n9.0.1 CLI, -c copy       1.333000s   40 frames
//
// # Why duration is the right instrument here, having been the wrong one for #58
//
// The frame count is already correct — every packet arrives. Only the reported
// length is wrong, so a count cannot see this, which is the mirror image of the
// #58 companion test, where duration could not tell a complete copy from a
// truncated one and had to be replaced by a count. The two defects need
// opposite instruments, and using either one for both is how each hid.

const (
	copyDurFrames = 45 // frames in the source clip
	copyDurRate   = 30 // frames per second
)

// copyDurWantSec is the clip's length by construction.
const copyDurWantSec = float64(copyDurFrames) / float64(copyDurRate)

func TestACopiedStreamKeepsItsFullLength(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)
			seq := writeClip(t, ws, "cd", copyDurFrames, 300)

			// The intermediate, encoded from a sequence whose length this test owns.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": seq, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(copyDurRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("cd.mkv"), "map": []any{"[v]"},
					"video_codec": "libopenh264",
				}},
				"filter": "[0:v]null[v]",
			})

			// Control. If the source is already short this is #12 on the encode lane,
			// and the copy below would only be inheriting it — a different defect
			// with a different fix, so it must not be reported as this one.
			src := probe(t, ws, a, ws.Path("cd.mkv")).Inputs[0].DurationSec
			if math.Abs(src-copyDurWantSec) > halfFrameSec {
				t.Fatalf("%s: the encoded source is %.6fs, want %.6fs ±%.6f — the copy below "+
					"cannot be judged against a source that is already wrong (this is #12, "+
					"not #59)", a, src, copyDurWantSec, halfFrameSec)
			}

			// The copy, with nothing else in the job.
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("cd.mkv")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("cd-copy.mkv"), "map": []any{"0:v"},
					"video_codec": "copy",
				}},
			})

			got := probe(t, ws, a, ws.Path("cd-copy.mkv")).Inputs[0].DurationSec
			if math.Abs(got-copyDurWantSec) > halfFrameSec {
				t.Errorf("%s: copying a %.6fs stream produced one reporting %.6fs, want "+
					"%.6fs ±%.6f — that is %.2f frames at %dfps (ffmpeg-wasi#59).\n"+
					"The source above measured %.6fs, so the length was right before the copy. "+
					"Every packet is still present; what is missing is the final sample's "+
					"duration, which no frame count can see.",
					a, copyDurWantSec, got, copyDurWantSec, halfFrameSec,
					math.Abs(got-copyDurWantSec)*copyDurRate, copyDurRate, src)
			}
		})
	}
}
