package conformance

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// Segmenting follows the caller's GOP — ffmpeg-wasi#62.
//
// The suite had no coverage of segmenting muxers at all: no hls, no dash, no
// segment. 668 conformance tests across ten artefacts passed straight through a
// real regression, and afmpeg's env-gated integration suite caught it instead —
// by hand, six days and two releases late.
//
// # What went unasserted
//
// A segmenter can only cut on a keyframe. Until #61 the engine handed the
// decoder's picture types to the encoder, so a re-encode of a keyframe-rich
// source came out as IDRs everywhere and segmenting worked whether or not the
// caller asked for a GOP. #61 stopped that, correctly — forcing picture types was
// the bug — and the encoder now chooses its own, which for openh264 is longer
// than a short clip. One keyframe, one segment, no error.
//
// # Why this asserts a relationship rather than a count
//
// A segment count is a property of the fixture, the frame rate and the encoder's
// defaults, and a threshold tuned to today's values is a number nobody can
// re-derive later. So the same source is encoded twice, differing only in the
// GOP, and the assertion is that the shorter GOP yields more segments.
//
// That needs no magic number, survives a fixture change, and fails for the right
// reason: an engine that forces keyframes segments identically at both GOPs, so
// the ratio collapses to 1 and the test says so. Verified red against n8.1.2-12
// and green against n9.0.1-3.

const (
	segFPS      = 25
	segFrames   = 75 // 3s at segFPS — several segments at segTimeSec
	segTimeSec  = 1
	segShortGOP = segFPS * segTimeSec // a keyframe on every segment boundary
	segLongGOP  = segFrames * 4       // far longer than the clip: one keyframe
)

// segmentingArtifacts skips an artifact that cannot segment. The lean profile
// carries no hls muxer, so it cannot exercise this at all.
func segmentingArtifacts(t *testing.T, a engine.Artifact) string {
	t.Helper()

	caps, err := engine.Query(context.Background(), a.Runner())
	if err != nil {
		t.Fatalf("%s: querying capabilities: %v", a, err)
	}
	if !slices.Contains(caps.Muxers, "hls") {
		t.Skipf("%s carries no hls muxer, so it cannot segment", a)
	}

	for _, enc := range []string{"libx264", "libopenh264"} {
		if slices.Contains(caps.Encoders, enc) {
			return enc
		}
	}
	t.Skipf("%s carries no H.264 encoder, so it has nothing to segment", a)

	return ""
}

func TestSegmentingFollowsTheCallersGOP(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			enc := segmentingArtifacts(t, a)
			ws := workspaceFor(t, a)

			for i := range segFrames {
				png, err := fixture.PNGPan(64, 48, i)
				if err != nil {
					t.Fatalf("building PNG %d: %v", i, err)
				}
				if _, err := ws.Write(fmt.Sprintf("s_%03d.png", i), png); err != nil {
					t.Fatalf("writing PNG %d: %v", i, err)
				}
			}

			// segmentsAt encodes the whole sequence to HLS at one GOP and counts the
			// segment files the muxer wrote. The playlist name differs per run so the
			// two cannot read each other's output.
			segmentsAt := func(tag string, gop int) int {
				t.Helper()

				runJob(t, ws, a, map[string]any{
					"op": "process",
					"inputs": []any{map[string]any{
						"path": ws.Path("s_%03d.png"), "format": "image2",
						"options": map[string]any{"framerate": fmt.Sprint(segFPS)},
					}},
					"filter": "[0:v]null,format=yuv420p[v]",
					"outputs": []any{map[string]any{
						"path": ws.Path(tag + ".m3u8"), "map": []any{"[v]"},
						"format": "hls", "video_codec": enc,
						// The common `options` rather than `video_options`: this output
						// opens only a video encoder, so the two are equivalent here, and
						// `options` is vocabulary 9 — which keeps the test meaningful when
						// it is run against an engine older than 0045. That matters for a
						// red proof: against n8.1.2-12 the GOP must genuinely reach the
						// encoder, so the only thing differing is the picture-type forcing.
						"options": map[string]any{"g": fmt.Sprint(gop)},
						"format_options": map[string]any{
							"hls_time":             fmt.Sprint(segTimeSec),
							"hls_segment_filename": ws.Path(tag + "_%03d.ts"),
							"hls_list_size":        "0",
						},
					}},
				})

				segs, err := ws.Glob(tag + "_*.ts")
				if err != nil {
					t.Fatalf("%s: globbing %s segments: %v", a, tag, err)
				}

				return len(segs)
			}

			short := segmentsAt("short", segShortGOP)
			long := segmentsAt("long", segLongGOP)

			// The long GOP is longer than the clip, so it can only produce one
			// keyframe and therefore one segment. Asserting that separately from the
			// comparison means a failure says WHICH half is wrong.
			if long != 1 {
				t.Errorf("%s: a GOP of %d over a %d-frame clip produced %d segments, want 1 — "+
					"the encoder is emitting keyframes the caller did not ask for",
					a, segLongGOP, segFrames, long)
			}

			if short <= long {
				t.Errorf("%s: g=%d produced %d segments and g=%d produced %d — segmenting does not "+
					"follow the caller's GOP (ffmpeg-wasi#62).\nAn engine that forces picture types "+
					"segments identically whatever the GOP, which is how #61 shipped a regression "+
					"through 668 passing tests.", a, segShortGOP, short, segLongGOP, long)
			}
		})
	}
}
