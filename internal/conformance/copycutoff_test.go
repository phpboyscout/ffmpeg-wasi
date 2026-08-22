package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// A stream-copy cutoff must not strand a reference — ffmpeg-wasi#55.
//
// `write_copy_pkt` decided the output window on PRESENTATION order. With
// reordered video that is the wrong axis: a reference frame can have a PTS at or
// after the cutoff while its DTS precedes a B-frame that depends on it. The
// reference is dropped, the dependent frame is inside the window and survives,
// and the job exits 0. The file plays to the end of the window and then the last
// GOP decodes with artefacts.
//
// # What the reference implementation does
//
// The ffmpeg CLI cuts a stream copy on DTS. Measured on a 100-frame libx264
// fixture with bframes=3 and b-pyramid, `-t 1.0 -c copy`:
//
//	packets kept    27      exactly the count with dts < 1.0 (pts < 1.0 gives 25)
//	max pts kept    1.12    past the window, deliberately
//	max dts kept    0.96
//
// So it keeps packets whose PRESENTATION time is beyond the window, which is
// precisely what keeps the references intact. The engine's own demux-side cutoff
// already cuts on DTS; only the copy lane disagreed, with itself.
//
// # What the assertion is, and the two weaker ones it replaced
//
// A frame count cannot see this: at a stranding cutoff the engine produced 18
// packets and 18 decoded frames — every packet present, one decoding from a
// picture that is not there.
//
// Comparing decoded pixels against the uncut source cannot see it either, or
// rather it sees too much: the LAST frame of any truncated reordered stream
// legitimately differs, because its forward reference is beyond the cut. The
// ffmpeg CLI's own output differs there too, so an assertion that demands every
// frame match fails against the reference implementation.
//
// What actually distinguishes them is the decoder saying so. A stranded
// reference produces `co located POCs unavailable`; a clean cut produces
// nothing. So this asserts on the decoder's own account of the file.

const (
	// cutoffFrames is long enough for several GOPs at cutoffRate, so a cut lands
	// mid-GOP where reordering actually matters.
	cutoffFrames = 100
	cutoffRate   = 25
)

// The cut points to try, as frame indices at cutoffRate.
//
// # Why this sweeps rather than naming the cutoff that fails
//
// The first version of this test hard-coded two cutoffs measured on a DIFFERENT
// fixture, and passed — it proved nothing, because whether a cut strands a
// reference depends on exactly where it lands in the GOP. On this fixture only
// ONE cut point in the swept range strands anything.
//
// That is also why the defect survived: it is not that reordered copies are
// usually broken, it is that they occasionally are, and any single sample is
// likely to miss it. A magic number measured elsewhere is worse than no test.
const (
	sweepFirstFrame = 10
	sweepLastFrame  = 50
)

func bframeArtifacts(t *testing.T, a engine.Artifact) {
	t.Helper()

	caps, err := engine.Query(context.Background(), a.Runner())
	if err != nil {
		t.Fatalf("%s: querying capabilities: %v", a, err)
	}
	// libx264 reorders by default; openh264 as built here does not, so an lgpl
	// artefact cannot produce the fixture this defect needs at all.
	if !slices.Contains(caps.Encoders, "libx264") {
		t.Skipf("%s carries no libx264, so it cannot encode the B-frames this needs", a)
	}
}

func TestACopyCutoffKeepsTheFramesItsSurvivorsReference(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			bframeArtifacts(t, a)

			ws := workspaceFor(t, a)

			// PNGPan, not PNG: this needs a stream the encoder can predict, so it
			// emits P and B frames and reorders them. PNG's frames differ wildly by
			// design — x264 calls a scene change on every one and emits all keyframes,
			// and a fixture with no reordering cannot show a defect about reordering.
			for i := range cutoffFrames {
				png, err := fixture.PNGPan(128, 96, i)
				if err != nil {
					t.Fatalf("building frame %d: %v", i, err)
				}
				if _, err := ws.Write(fmt.Sprintf("cc_%02d.png", i), png); err != nil {
					t.Fatalf("writing frame %d: %v", i, err)
				}
			}
			seq := ws.Path("cc_%02d.png")

			// A real reordered stream. Without B-frames there is no difference between
			// the two axes and this test would pass against the defect.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": seq, "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(cutoffRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("src.mp4"), "map": []any{"[v]"},
					"video_codec": "libx264",
					// b-pyramid makes a B-frame itself a reference, which is the
					// arrangement that lets a high-PTS packet be needed by a
					// low-DTS one. Without it the two axes cannot disagree in a way
					// that matters, and the fixture cannot show the defect at all.
					"options": map[string]any{"bf": "3", "g": "25", "x264opts": "b-pyramid=normal"},
				}},
				"filter": "[0:v]format=yuv420p[v]",
			})

			// Control: the uncut fixture decodes without complaint. Without this a
			// clean decode below could mean the fixture never had references to
			// strand.
			if _, stderr := decodeCounting(t, ws, a, "src.mp4", "full"); decoderComplained(stderr) {
				t.Fatalf("%s: the uncut fixture already decodes with errors, so a complaint "+
					"about a cut would not be the cut's fault.\n%s", a, strings.TrimSpace(stderr))
			}

			for f := sweepFirstFrame; f <= sweepLastFrame; f++ {
				cut := float64(f) / float64(cutoffRate)
				out := fmt.Sprintf("cut%03d.mp4", f)

				runJob(t, ws, a, map[string]any{
					"op":     "process",
					"inputs": []any{map[string]any{"path": ws.Path("src.mp4")}},
					"outputs": []any{map[string]any{
						"path": ws.Path(out), "map": []any{"0:v"},
						"video_codec": "copy", "duration": cut,
					}},
				})

				n, stderr := decodeCounting(t, ws, a, out, fmt.Sprintf("c%03d", f))
				if n == 0 {
					t.Errorf("%s: a %.2fs copy window produced nothing", a, cut)

					continue
				}

				if decoderComplained(stderr) {
					t.Errorf("%s: a %.2fs copy window produced a file the decoder cannot "+
						"fully decode (ffmpeg-wasi#55).\n"+
						"Every packet is present — %d of them — but one decodes from a "+
						"reference the cutoff dropped. The window was decided on presentation "+
						"order; a reference can sit at or after the cutoff in PTS while its DTS "+
						"precedes a B-frame that survives.\n%s",
						a, cut, n, strings.TrimSpace(stderr))

					break
				}
			}
		})
	}
}

// decoderComplained reports whether libavcodec said it could not decode
// something. The marker is specific: a stranded reference in H.264 says the
// co-located picture is unavailable, and a clean file says nothing at all.
// Advisory warnings from encoders and filters are noise here and are not matched.
func decoderComplained(stderr string) bool {
	for _, marker := range []string{
		"co located POCs unavailable",
		"reference picture missing",
		"error while decoding",
		"no frame",
	} {
		if strings.Contains(strings.ToLower(stderr), strings.ToLower(marker)) {
			return true
		}
	}

	return false
}

// decodeCounting decodes a file to one PNG per frame and returns how many frames
// came out, plus everything the decoder said while doing it.
func decodeCounting(t *testing.T, ws *engine.Workspace, a engine.Artifact, in, tag string) (int, string) {
	t.Helper()

	spec, err := json.Marshal(map[string]any{
		"op":     "process",
		"inputs": []any{map[string]any{"path": ws.Path(in)}},
		"outputs": []any{map[string]any{
			"path": ws.Path(tag + "_%04d.png"), "map": []any{"[v]"}, "video_codec": "png",
		}},
		"filter": "[0:v]null[v]",
	})
	if err != nil {
		t.Fatal(err)
	}

	res := runAndCheck(t, ws.Runner(), t.Context(), string(spec), "decoding "+in)
	if res.ExitCode != 0 {
		t.Fatalf("%s: decoding %s failed (exit %d)\n%s", a, in, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	names, err := ws.Glob(tag + "_*.png")
	if err != nil {
		t.Fatalf("%s: listing %s frames: %v", a, tag, err)
	}

	return len(names), res.Stderr
}
