package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// Concat playlist quoting — ffmpeg-wasi#25.
//
// The engine builds an ffconcat playlist by interpolating segment names into
// `file '<name>'` with no escaping. An apostrophe closes the quote early, so an
// ordinary filename breaks; a newline injects a playlist directive, which is the
// security half.
//
// The apostrophe is the case tested here because it needs no adversary — "it's
// a wrap.mkv" is a filename a person types. If quoting is right for that, the
// injection case cannot arise either, since both are the same missing escape.

// concatSegments writes n identical single-second clips under names of the
// caller's choosing, and returns the directory serving them.
func concatSegments(t *testing.T, a engine.Artifact, names []string) (*ipchost.Host, string) {
	t.Helper()

	// Build one real clip through the engine, then place it under each name.
	build := workspaceFor(t, a)
	mp4Fixture(t, build, a) // writes clip.mkv into the workspace
	body, err := build.Read("clip.mkv")
	if err != nil {
		t.Fatalf("%s: reading the segment: %v", a, err)
	}

	root := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(root, n), body, 0o644); err != nil {
			t.Fatalf("%s: writing segment %q: %v", a, n, err)
		}
	}

	h, sock, err := ipchost.Listen(root, t.TempDir())
	if err != nil {
		t.Fatalf("%s: starting the host: %v", a, err)
	}
	t.Cleanup(func() { _ = h.Close() })

	return h, sock
}

func runConcat(t *testing.T, a engine.Artifact, sock string, segs []string) (int, string) {
	t.Helper()

	job, err := json.Marshal(map[string]any{
		"op":      "process",
		"version": 2,
		"inputs":  []any{map[string]any{"concat": segs}},
		"outputs": []any{map[string]any{
			"path": "joined.mkv", "map": []any{"0:v"}, "video_codec": "copy",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
		Run(ctx, string(job))
	if ctx.Err() != nil {
		t.Fatalf("%s: the concat job hung", a)
	}
	if err != nil {
		t.Fatalf("%s: invoking: %v", a, err)
	}
	return res.ExitCode, res.Stderr
}

// TestConcatQuotesASegmentNameContainingAnApostrophe is the regression test
// for #25.
func TestConcatQuotesASegmentNameContainingAnApostrophe(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// Control: ordinary names must join. Without this a failure below could
			// be concat not working at all rather than the quoting.
			_, sock := concatSegments(t, a, []string{"a.mkv", "b.mkv"})
			if code, stderr := runConcat(t, a, sock, []string{"a.mkv", "b.mkv"}); code != 0 {
				t.Fatalf("%s: joining two ordinary names failed (exit %d) — concat is broken "+
					"before quoting is reached.\n%s", a, code, strings.TrimSpace(stderr))
			}

			// The same join, with an apostrophe in one name.
			const quoted = "it's a wrap.mkv"
			h2, sock2 := concatSegments(t, a, []string{"a.mkv", quoted})

			code, stderr := runConcat(t, a, sock2, []string{"a.mkv", quoted})
			if code != 0 {
				t.Fatalf("%s: a segment named %q broke the join (exit %d) — the playlist "+
					"interpolates names into `file '<name>'` without escaping, so the apostrophe "+
					"closes the quote early (ffmpeg-wasi#25).\n%s",
					a, quoted, code, strings.TrimSpace(stderr))
			}

			// Exit 0 is not enough. An engine that kept the quoting bug but swallowed
			// the resulting error, or wrote a file holding only the first segment,
			// would satisfy an exit-code check. The join has to have actually
			// happened, so require BOTH segments' worth of material.
			one := concatFrameCount(t, a, h2, sock2, []string{"a.mkv"})
			two := concatFrameCount(t, a, h2, sock2, []string{"a.mkv", quoted})

			// Guard the baseline first. With one == 0 the comparison below is
			// "0 < 0", which is false, and the whole assertion passes having
			// measured nothing — the exact shape of vacuous pass this suite exists
			// to catch, and one I nearly shipped writing the check for it.
			if one == 0 {
				t.Fatalf("%s: counting a single-segment join yielded no frames, so the "+
					"comparison below would pass without measuring anything", a)
			}
			// Half again is enough to prove the second segment contributed, without
			// demanding an exact doubling the muxer is not obliged to give.
			if two < one+one/2 {
				t.Errorf("%s: joining a.mkv with %q yielded %d frames, but a.mkv alone yields %d — "+
					"the second segment did not reach the output, so the join failed quietly "+
					"rather than loudly (ffmpeg-wasi#25).", a, quoted, two, one)
			}
		})
	}
}

// TestConcatDoesNotLetAFilenameInjectAPlaylistDirective is the security half of
// #25. A newline in a name would otherwise start a new ffconcat line, and the
// concat demuxer honours directives — so a caller-chosen filename becomes
// control over what the engine opens.
//
// The assertion is deliberately weak on purpose: it does not require a specific
// error, only that the engine does not silently act on the injected directive.
// A filename containing a newline is unusual enough that refusing it outright is
// a legitimate fix, and so is escaping it.
func TestConcatDoesNotLetAFilenameInjectAPlaylistDirective(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// The name closes its own quote, adds a directive for a file that does
			// not exist, and leaves a dangling quote for the engine's trailing one.
			injected := "a.mkv'\nfile 'no-such-segment.mkv"

			h, sock := concatSegments(t, a, []string{"a.mkv"})

			// Control first. Without it, an unrelated concat failure — a broken
			// fixture, a missing muxer — satisfies the assertion below while the
			// unsafe interpolation is still there, because "did not open the
			// injected name" is also true of a job that never got started.
			if code, stderr := runConcat(t, a, sock, []string{"a.mkv"}); code != 0 {
				t.Fatalf("%s: joining one ordinary segment failed (exit %d) — concat is broken "+
					"before injection is reached, so this test would pass for the wrong reason.\n%s",
					a, code, strings.TrimSpace(stderr))
			}

			code, stderr := runConcat(t, a, sock, []string{injected})

			// Either outcome is acceptable; what is not is the engine opening
			// ANYTHING the caller did not name.
			//
			// A whitelist rather than a search for "no-such-segment": that string is
			// this test's own payload, and checking only for it would miss an
			// injection through a different directive — `file` is not the only one
			// the concat demuxer honours. Anything outside the set below is the
			// filename having become control over what the engine opens.
			allowed := map[string]bool{
				"a.mkv":      true, // the real segment, and the control job above
				"joined.mkv": true, // the job's own output, which also crosses the bridge
				injected:     true, // asking for the literal name is fine; acting on it is not
			}
			for _, opened := range h.Opened() {
				if !allowed[opened] {
					t.Errorf("%s: a newline in a segment name made the engine open %q, which the "+
						"caller never asked for (ffmpeg-wasi#25).\nexit %d, stderr: %s",
						a, opened, code, strings.TrimSpace(stderr))
				}
			}
		})
	}
}

// concatFrameCount joins segs and returns how many frames the result yields.
//
// It counts frames rather than probing: a probe asks the engine what it thinks it
// wrote, and a quietly-failed join is exactly the case where that answer cannot be
// trusted. The output is an image sequence over the same bridge, so the host's own
// record of what it was asked to open is the count.
func concatFrameCount(t *testing.T, a engine.Artifact, h *ipchost.Host, sock string, segs []string) int {
	t.Helper()

	before := len(h.Opened())
	job, err := json.Marshal(map[string]any{
		"op":      "process",
		"version": 2,
		"inputs":  []any{map[string]any{"concat": segs}},
		"outputs": []any{map[string]any{
			"path": "d_%04d.png", "map": []any{"[v]"}, "video_codec": "png",
		}},
		"filter": "[0:v]null[v]",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()
	res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
		Run(ctx, string(job))
	if err != nil {
		t.Fatalf("%s: invoking: %v", a, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("%s: counting the join of %v failed (exit %d)\n%s",
			a, segs, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	n := 0
	for _, name := range h.Opened()[before:] {
		if strings.HasPrefix(name, "d_") {
			n++
		}
	}
	return n
}
