package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// A v2 engine must still work against a v1 host — afmpeg spec 0041 D1.
//
// D1 says an engine speaking v2 to a host speaking v1 must DEGRADE, not fail.
// The first implementation did not: it announced v2, got no answer, and gave up.
//
// That was found by rendering keryx's real reel against a driver built from
// main. keryx pins afmpeg v0.14.0, whose host is v1, and every render failed
// with "cannot open input seg0.png" — a released consumer broken by an
// unreleased engine, which is exactly what D5's ordering exists to prevent and
// what the ordering alone could not have prevented, because afmpeg's v2 host is
// merged but not yet released.
//
// # Why the fallback has to be a retry
//
// A v1 host has no negotiation. It validates the version byte, finds one it does
// not know, and CLOSES. It cannot answer "I speak 1", because answering at all
// is a v2 behaviour. From the engine's side that is a failed read, which is
// indistinguishable from a host that died — so the only way to tell is to try
// again as v1, and the only way to avoid paying that per file is to remember it
// for the rest of the job.
func TestTheEngineDegradesToAVersionOneHost(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			run := func(maxVersion byte) engine.Result {
				t.Helper()

				served := t.TempDir()
				png, err := fixture.PNGPan(64, 48, 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(served, "in.png"), png, 0o644); err != nil {
					t.Fatal(err)
				}

				h, sock, err := ipchost.Listen(served, t.TempDir())
				if err != nil {
					t.Fatalf("%s: starting the host: %v", a, err)
				}
				h.MaxVersion = maxVersion
				t.Cleanup(func() { _ = h.Close() })

				spec, err := json.Marshal(map[string]any{
					"op":     "process",
					"inputs": []any{map[string]any{"path": "in.png"}},
					"outputs": []any{map[string]any{
						"path": "out.mkv", "map": []any{"[v]"}, "video_codec": "libopenh264",
					}},
					"filter": "[0:v]null[v]",
				})
				if err != nil {
					t.Fatal(err)
				}

				ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
				defer cancel()

				res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
					Run(ctx, string(spec))
				if ctx.Err() != nil {
					t.Fatalf("%s: the job hung against a host capped at v%d", a, maxVersion)
				}
				if err != nil {
					t.Fatalf("%s: invoking: %v", a, err)
				}

				return res
			}

			// Control: the current host. Without it a pass below could mean the job
			// never worked over the bridge at all.
			if res := run(0); res.ExitCode != 0 {
				t.Fatalf("%s: the job failed against a CURRENT host (exit %d), so the "+
					"v1 case below proves nothing.\n%s", a, res.ExitCode,
					strings.TrimSpace(res.Stderr))
			}

			res := run(1)
			if res.ExitCode != 0 {
				t.Errorf("%s: the job failed against a host that only speaks v1 (exit %d).\n"+
					"A released afmpeg is a v1 host, so this is every existing consumer "+
					"(afmpeg spec 0041 D1).\n%s", a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}

			// It must SAY it degraded. A silent downgrade is how a caller ends up
			// believing they have a guarantee the session does not provide.
			if !strings.Contains(res.Stderr, "falling back to v1") {
				t.Errorf("%s: the engine degraded without saying so — stderr should name "+
					"the fallback so an operator can see which contract the job ran under.\n%s",
					a, strings.TrimSpace(res.Stderr))
			}
		})
	}
}
