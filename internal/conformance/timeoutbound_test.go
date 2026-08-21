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

// The IPC bound has no off switch — afmpeg spec 0044 D5, ffmpeg-wasi#60.
//
// AFIO_TIMEOUT_SECS bounds one read() or write() on the bridge socket, which is
// the right granularity: a host serving a large seek may legitimately take a
// while, and bounding a whole job would fail slow work rather than stalled work.
// AFMPEG_NATIVE_TIMEOUT overrides the 120s default, and that stays.
//
// What goes is `0`, which used to mean "never time out". Under spec 0043 the
// engine runs behind a Landlock floor, and a denial there produces an error a
// component can sit on — so block-forever becomes a documented way to turn a
// sandbox denial into an indefinite hang. It was the only setting that removed
// the sole bound on a blocked bridge, and no host is known to have wanted it.
//
// # Why this refuses rather than silently substituting the default
//
// A caller who wrote 0 believes there is no deadline. Quietly applying 120s
// would leave them believing something untrue about their own job, which is the
// shape of half the defects on this branch.

func TestTheIPCTimeoutCannotBeDisabled(t *testing.T) {
	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			build := workspaceFor(t, a)
			mp4Fixture(t, build, a)
			body, err := build.Read("clip.mkv")
			if err != nil {
				t.Fatalf("%s: reading the fixture: %v", a, err)
			}

			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "in.mkv"), body, 0o644); err != nil {
				t.Fatal(err)
			}
			h, sock, err := ipchost.Listen(root, t.TempDir())
			if err != nil {
				t.Fatalf("%s: starting the host: %v", a, err)
			}
			t.Cleanup(func() { _ = h.Close() })

			job, err := json.Marshal(map[string]any{
				"op": "process", "version": 2,
				"inputs": []any{map[string]any{"path": "in.mkv"}},
				"outputs": []any{map[string]any{
					"path": "out.mkv", "map": []any{"0:v"}, "video_codec": "copy",
				}},
			})
			if err != nil {
				t.Fatal(err)
			}

			run := func(timeout string) engine.Result {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
				defer cancel()
				res, err := engine.Native{Path: a.Path, Env: []string{
					"AFMPEG_NATIVE_SOCKET=" + sock,
					"AFMPEG_NATIVE_TIMEOUT=" + timeout,
				}}.Run(ctx, string(job))
				if ctx.Err() != nil {
					t.Fatalf("%s: the job hung with AFMPEG_NATIVE_TIMEOUT=%s", a, timeout)
				}
				if err != nil {
					t.Fatalf("%s: invoking: %v", a, err)
				}
				return res
			}

			// Control first. A finite override must still work, or the assertion
			// below would pass against an engine that refuses the variable entirely.
			if res := run("300"); res.ExitCode != 0 {
				t.Fatalf("%s: a finite AFMPEG_NATIVE_TIMEOUT was refused (exit %d) — the "+
					"override itself is meant to keep working.\nstderr: %s",
					a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}

			res := run("0")
			if res.ExitCode == 0 {
				t.Errorf("%s: AFMPEG_NATIVE_TIMEOUT=0 was accepted, so the bridge has no "+
					"deadline at all. Under spec 0043 that is a documented way to turn a "+
					"sandbox denial into an indefinite hang (ffmpeg-wasi#60).", a)
			} else if !strings.Contains(res.Stderr, "AFMPEG_NATIVE_TIMEOUT") {
				t.Errorf("%s: refused, but stderr does not name the variable: %q",
					a, strings.TrimSpace(res.Stderr))
			}
		})
	}
}
