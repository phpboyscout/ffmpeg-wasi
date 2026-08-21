package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// The Landlock floor — afmpeg spec 0043 D2 and D3, ffmpeg-wasi#13.
//
// libav reaches the filesystem through four channels. The bridge covers
// AVFormatContext.pb and, ad hoc, io_open child opens. It cannot see a plain
// fopen inside a filter, and no seam in this codebase can: that is libc inside
// third-party C. Only the kernel is below all of it.
//
// # Why the floor is only installed when the bridge is serving I/O
//
// A refinement the spec does not spell out, and forced rather than chosen. In
// plain-file mode the caller hands the engine real host paths and there is no
// containment claim to enforce; sandboxing that would break the ordinary way the
// driver is invoked, and granting the job spec's paths would be the
// classification table D5 rejects.

// lutFixture writes a 3D LUT that maps every colour to solid green.
//
// Content, not an exit code: a job that reads THIS file produces green pixels,
// and one that reads a different file does not. That is what makes the pair
// below a proof about which filesystem was consulted rather than about whether
// something failed.
const lutFixture = "LUT_3D_SIZE 2\n" +
	"0 1 0\n0 1 0\n0 1 0\n0 1 0\n0 1 0\n0 1 0\n0 1 0\n0 1 0\n"

func sandboxArtifacts(t *testing.T, a engine.Artifact) {
	t.Helper()

	caps, err := engine.Query(context.Background(), a.Runner())
	if err != nil {
		t.Fatalf("%s: querying capabilities: %v", a, err)
	}
	if !slices.Contains(caps.Filters, "lut3d") {
		t.Skipf("%s carries no lut3d, so it has no channel-4 vector to contain", a)
	}
}

// TestTheSandboxDeniesAFilterTheHostFilesystem is the paired canary D2 asks for.
//
// The two runs differ in ONE thing: whether the sandbox is installed. That is
// what makes it a proof of causation rather than an observation that something
// failed — a job can fail for a hundred reasons, and only the pair says which.
//
// Measured on the native intermediate/gpl driver:
//
//	sandbox on    exit 1
//	sandbox off   exit 0, and the filter read the host LUT
//
// `lut3d=file=` is one of the eight path-taking filter options 0043 enumerates.
// It reaches host disk through libc, below every seam this project owns, and the
// bridge has never been able to see it.
func TestTheSandboxDeniesAFilterTheHostFilesystem(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			sandboxArtifacts(t, a)

			// The LUT lives on the host, NOT in the served directory. Nothing the
			// caller gave the engine names it.
			host := t.TempDir()
			lut := filepath.Join(host, "green.cube")
			if err := os.WriteFile(lut, []byte(lutFixture), 0o644); err != nil {
				t.Fatal(err)
			}

			run := func(sandbox bool) engine.Result {
				t.Helper()

				served := t.TempDir()
				png, err := fixture.PNG(64, 48, 3)
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
				t.Cleanup(func() { _ = h.Close() })

				spec := `{"op":"process","inputs":[{"path":"in.png","format":"image2",` +
					`"options":{"framerate":"25"}}],` +
					`"filter":"[0:v]format=rgb24,lut3d=file=` + lut + `[v]",` +
					`"outputs":[{"path":"out.png","map":["[v]"],"video_codec":"png"}]}`

				env := []string{"AFMPEG_NATIVE_SOCKET=" + sock}
				if !sandbox {
					env = append(env, "AFMPEG_NO_SANDBOX=1")
				}

				res, err := engine.Native{Path: a.Path, Env: env}.Run(context.Background(), spec)
				if err != nil {
					t.Fatalf("%s: invoking: %v", a, err)
				}

				return res
			}

			// Sandbox OFF: the escape is real and still there. Without this half the
			// test would pass against a build where lut3d simply does not work, or
			// where the fixture is wrong — neither of which is containment.
			if res := run(false); res.ExitCode != 0 {
				t.Fatalf("%s: with the sandbox disabled the job failed (exit %d), so the "+
					"pair below proves nothing: it must be the SANDBOX that denies the "+
					"access, not a broken fixture.\nstderr: %s",
					a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}

			// Sandbox ON: denied.
			if res := run(true); res.ExitCode == 0 {
				t.Errorf("%s: a filter read a host file the caller never named, with the "+
					"sandbox installed (ffmpeg-wasi#13).\nlut3d opens its file through libc, "+
					"below every seam this project owns — if the kernel does not stop it, "+
					"nothing does.", a)
			}
		})
	}
}

// TestTheSandboxStateIsReported is D3: a host that requires confinement must be
// able to ASSERT it rather than assume it.
//
// Both places, because they answer different questions. --capabilities is what a
// host checks when it picks a driver; the job result is what it checks about the
// job it just ran, and a build that lost its sandbox between those two moments
// would otherwise look fine.
func TestTheSandboxStateIsReported(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			served := t.TempDir()
			h, sock, err := ipchost.Listen(served, t.TempDir())
			if err != nil {
				t.Fatalf("%s: starting the host: %v", a, err)
			}
			t.Cleanup(func() { _ = h.Close() })

			caps, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
				Run(context.Background(), "--capabilities")
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}

			var got struct {
				Sandbox string `json:"sandbox"`
			}
			if err := json.Unmarshal([]byte(caps.Stdout), &got); err != nil {
				t.Fatalf("%s: parsing --capabilities: %v", a, err)
			}

			// "unavailable" is a legitimate answer on a kernel without Landlock, and
			// is why the driver runs rather than refusing to start (0043 OQ1). What is
			// not legitimate is the field being absent or empty, which is the state a
			// host cannot distinguish from an old driver that never had a sandbox.
			switch got.Sandbox {
			case "landlock", "unavailable":
			case "":
				t.Errorf("%s: --capabilities reports no sandbox state at all, so a host "+
					"requiring confinement cannot tell this driver from one that never had "+
					"any (ffmpeg-wasi#13)", a)
			default:
				t.Errorf("%s: --capabilities reports sandbox %q with the bridge active; "+
					"want landlock, or unavailable on a kernel without it", a, got.Sandbox)
			}
		})
	}
}

// TestTheSandboxDoesNotBreakMatroska is the false-positive direction.
//
// A grant list can be wrong by being too tight as easily as too loose, and the
// failure then is a working job that stops working. Matroska asks
// av_get_random_seed for a segment UID; without a readable /dev/urandom that call
// does not fail, it BLOCKS — which is how mkv output once hung under wasi.
func TestTheSandboxDoesNotBreakMatroska(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			served := t.TempDir()
			png, err := fixture.PNG(64, 48, 5)
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
			t.Cleanup(func() { _ = h.Close() })

			spec := `{"op":"process","inputs":[{"path":"in.png","format":"image2",` +
				`"options":{"framerate":"25"}}],"filter":"[0:v]null[v]",` +
				`"outputs":[{"path":"out.mkv","map":["[v]"],"video_codec":"libopenh264"}]}`

			ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
			defer cancel()

			res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
				Run(ctx, spec)
			if ctx.Err() != nil {
				t.Fatalf("%s: the Matroska job HUNG under the sandbox — /dev/urandom is "+
					"granted precisely so av_get_random_seed does not block", a)
			}
			if err != nil {
				t.Fatalf("%s: invoking: %v", a, err)
			}
			if res.ExitCode != 0 {
				t.Errorf("%s: muxing Matroska failed under the sandbox (exit %d). The grant "+
					"list is wrong in the other direction — too tight rather than too loose, "+
					"which breaks working jobs instead of leaking.\nstderr: %s",
					a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}
		})
	}
}
