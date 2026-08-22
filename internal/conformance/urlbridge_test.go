package conformance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// The URL-protocol layer over the bridge — afmpeg spec 0043 D1, closing
// ffmpeg-wasi#36 and #35.
//
// The bridge covered AVFormatContext.pb and, ad hoc, io_open child opens. It
// could not see the URLs libav opens on its OWN initiative, so:
//
//   - a numbered image sequence could not be read at all. image2 is AVFMT_NOFILE:
//     it expands the pattern and opens each frame itself, and libav says so —
//     "Custom AVIOContext makes no sense and will be ignored with AVFMT_NOFILE
//     format". The engine handed it one anyway and dialled the PATTERN as a
//     filename, so the host was asked for `s_%03d.png` and quite rightly had none.
//   - HLS left its playlist as a .tmp, because ff_rename never reached the host.
//
// Both are now the same seam: a carried patch routes the `file` protocol through
// the bridge on the native build, so probe and open resolve against the same
// filesystem. That is what #36 actually was — not a missing capability but a
// DISAGREEMENT between two layers about which filesystem exists.

// bridged serves dir over a fresh host and runs one job against it.
func bridged(t *testing.T, a engine.Artifact, dir, spec string) (engine.Result, *ipchost.Host) {
	t.Helper()

	h, sock, err := ipchost.Listen(dir, t.TempDir())
	if err != nil {
		t.Fatalf("%s: starting the host: %v", a, err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
		Run(ctx, spec)
	if ctx.Err() != nil {
		t.Fatalf("%s: the job hung", a)
	}
	if err != nil {
		t.Fatalf("%s: invoking: %v", a, err)
	}

	return res, h
}

// writeSequenceInto puts n predictable frames into dir and returns the pattern.
func writeSequenceInto(t *testing.T, dir string, n int) string {
	t.Helper()

	for i := range n {
		png, err := fixture.PNGPan(64, 48, i)
		if err != nil {
			t.Fatalf("building frame %d: %v", i, err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("s_%03d.png", i)), png, 0o644); err != nil {
			t.Fatalf("writing frame %d: %v", i, err)
		}
	}

	return "s_%03d.png"
}

// TestAnImageSequenceCanBeReadOverTheBridge closes #36.
//
// Its counterpart, TestAnImageSequenceCannotReachHostDisk, must stay green
// alongside it: the capability and the containment are separate claims, and a
// change that delivered one by giving up the other would be the sandbox escape
// this defect already produced once when it was fixed the obvious way.
func TestAnImageSequenceCanBeReadOverTheBridge(t *testing.T) {
	t.Parallel()

	const frames = 8

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			served := t.TempDir()
			pattern := writeSequenceInto(t, served, frames)

			res, h := bridged(t, a, served, fmt.Sprintf(
				`{"op":"process","inputs":[{"path":%q,"format":"image2",`+
					`"options":{"framerate":"25"}}],"filter":"[0:v]null[v]",`+
					`"outputs":[{"path":"out_%%03d.png","map":["[v]"],"video_codec":"png"}]}`,
				pattern))
			if res.ExitCode != 0 {
				t.Fatalf("%s: a numbered sequence could not be read over the bridge "+
					"(exit %d, ffmpeg-wasi#36).\n%s", a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}

			// Every frame arrived. Exit 0 alone would be satisfied by a job that read
			// one image and stopped.
			out, err := filepath.Glob(filepath.Join(served, "out_*.png"))
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != frames {
				t.Errorf("%s: the sequence produced %d frames, want %d", a, len(out), frames)
			}

			// And the HOST was asked for them. This is the half that matters: the
			// engine could have produced the right answer by reading host disk, which
			// is exactly the escape the first attempt at #36 shipped.
			asked := 0
			for _, n := range h.Opened() {
				if strings.HasPrefix(n, "s_") {
					asked++
				}
			}
			if asked < frames {
				t.Errorf("%s: the host was asked for only %d of %d source frames — the rest "+
					"came from somewhere the caller did not serve (ffmpeg-wasi#36)", a, asked, frames)
			}
		})
	}
}

// TestHLSRenamesItsPlaylistOverTheBridge closes #35.
//
// HLS writes the playlist to a .tmp and renames it, so a concurrent reader sees
// a whole file or the previous one. The protocol had no rename, so over the
// bridge the segments were written correctly and the playlist naming them was
// absent — the output was contained but unusable.
func TestHLSRenamesItsPlaylistOverTheBridge(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}
			if !slices.Contains(caps.Muxers, "hls") {
				t.Skipf("%s carries no hls muxer", a)
			}

			served := t.TempDir()
			pattern := writeSequenceInto(t, served, 30)

			res, _ := bridged(t, a, served, fmt.Sprintf(
				`{"op":"process","inputs":[{"path":%q,"format":"image2",`+
					`"options":{"framerate":"25"}}],"filter":"[0:v]null[v]",`+
					`"outputs":[{"path":"stream.m3u8","map":["[v]"],"video_codec":"libopenh264",`+
					`"format_options":{"hls_time":"0.4","hls_segment_filename":"seg_%%03d.ts"}}]}`,
				pattern))
			if res.ExitCode != 0 {
				t.Fatalf("%s: the HLS job failed (exit %d)\n%s", a, res.ExitCode,
					strings.TrimSpace(res.Stderr))
			}

			// The playlist, under the name a player would ask for.
			if _, err := os.Stat(filepath.Join(served, "stream.m3u8")); err != nil {
				left, _ := filepath.Glob(filepath.Join(served, "*.tmp"))
				t.Errorf("%s: no stream.m3u8 in the served filesystem (ffmpeg-wasi#35).\n"+
					"Left behind instead: %v — which is the .tmp the muxer meant to rename.",
					a, left)
			}

			// A playlist naming no segments is not a working ladder, and the rename
			// could succeed while the segments went elsewhere.
			segs, _ := filepath.Glob(filepath.Join(served, "seg_*.ts"))
			if len(segs) == 0 {
				t.Errorf("%s: the playlist exists but no segments were served alongside it", a)
			}
		})
	}
}
