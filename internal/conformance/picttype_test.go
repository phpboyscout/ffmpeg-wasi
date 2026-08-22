package conformance

import (
	"fmt"
	"slices"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// The encoder chooses its own picture types — ffmpeg-wasi#61.
//
// The engine handed the decoder's `pict_type` to the encoder, and libx264 obeys
// it: a frame arriving as AV_PICTURE_TYPE_I is forced to an IDR. An image
// sequence decodes as all-I by construction, so every output frame became a
// keyframe and the encoder never got to choose a P or a B.
//
// Measured before the fix, 100 frames of a block sliding across a gradient:
//
//	engine      34727 bytes    99 I, 1 I
//	ffmpeg CLI   7287 bytes    57 B, 39 P, 3 I, 1 I
//
// 4.8x the size from identical input frames. It hid because a normal video input
// reproduces its OWN types — a source with 65 B / 31 P / 3 I comes out as
// exactly that — so the output always looked plausible.
//
// # Why this asserts a ratio rather than a size
//
// A byte count is a property of the fixture, and a threshold tuned to one is a
// number nobody can re-derive later. So the test encodes the same frames twice
// and compares them to each other: once asking for a 25-frame GOP, once asking
// for all keyframes.
//
// A working engine makes those two very different. A broken one makes them
// identical — measured at a ratio of 1.000, because the GOP setting could not
// change anything when every frame was already forced to a keyframe. The
// assertion needs no magic number, and it stays true if the fixture changes.

const (
	pictFrames = 60 // enough for several GOPs at pictGOP
	pictGOP    = 25
)

func TestTheEncoderChoosesItsOwnPictureTypes(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(t.Context(), a.Runner())
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}
			// Needs an encoder that both honours pict_type and uses B-frames when
			// left alone. openh264 as built here does neither, so it cannot show the
			// difference either way.
			if !slices.Contains(caps.Encoders, "libx264") {
				t.Skipf("%s carries no libx264, so it cannot show a forced picture type", a)
			}

			ws := workspaceFor(t, a)
			for i := range pictFrames {
				png, err := fixture.PNGPan(128, 96, i)
				if err != nil {
					t.Fatalf("building frame %d: %v", i, err)
				}
				if _, err := ws.Write(fmt.Sprintf("p_%02d.png", i), png); err != nil {
					t.Fatalf("writing frame %d: %v", i, err)
				}
			}

			encode := func(name string, gop int) int {
				t.Helper()

				runJob(t, ws, a, map[string]any{
					"op": "process",
					"inputs": []any{map[string]any{
						"path": ws.Path("p_%02d.png"), "format": "image2",
						"options": map[string]any{"framerate": "25"},
					}},
					"outputs": []any{map[string]any{
						"path": ws.Path(name), "map": []any{"[v]"},
						"video_codec": "libx264",
						"options":     map[string]any{"bf": "3", "g": fmt.Sprint(gop)},
					}},
					"filter": "[0:v]format=yuv420p[v]",
				})

				body, err := ws.Read(name)
				if err != nil {
					t.Fatalf("%s: reading %s: %v", a, name, err)
				}

				return len(body)
			}

			inter := encode("inter.mp4", pictGOP)
			allKey := encode("allkey.mp4", 1)

			if allKey == 0 {
				t.Fatalf("%s: the all-keyframe encode produced nothing, so the comparison "+
					"below would divide by zero and prove nothing", a)
			}

			// Half is a wide margin: the measured difference is closer to a fifth. What
			// it must not be is ~1.0, which is what a forced picture type produces.
			if ratio := float64(inter) / float64(allKey); ratio > 0.5 {
				t.Errorf("%s: asking for a %d-frame GOP produced %d bytes against %d for "+
					"all-keyframes — a ratio of %.3f (ffmpeg-wasi#61).\n"+
					"The GOP setting changed nothing, which means every frame was a keyframe "+
					"either way: the decoder's picture type is being forced onto the encoder, "+
					"and an image sequence decodes as all-I by construction.",
					a, pictGOP, inter, allKey, ratio)
			}
		})
	}
}
