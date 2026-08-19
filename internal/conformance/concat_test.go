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
			_, sock2 := concatSegments(t, a, []string{"a.mkv", quoted})

			code, stderr := runConcat(t, a, sock2, []string{"a.mkv", quoted})
			if code != 0 {
				t.Errorf("%s: a segment named %q broke the join (exit %d) — the playlist "+
					"interpolates names into `file '<name>'` without escaping, so the apostrophe "+
					"closes the quote early (ffmpeg-wasi#25).\n%s",
					a, quoted, code, strings.TrimSpace(stderr))
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
			code, stderr := runConcat(t, a, sock, []string{injected})

			// Either outcome is acceptable; what is not is the engine opening the
			// injected name as if the caller had asked for it.
			for _, opened := range h.Opened() {
				if strings.Contains(opened, "no-such-segment") {
					t.Errorf("%s: a newline in a segment name injected a playlist directive and "+
						"the engine acted on it — it tried to open %q (ffmpeg-wasi#25).\n"+
						"exit %d, stderr: %s", a, opened, code, strings.TrimSpace(stderr))
				}
			}
		})
	}
}
