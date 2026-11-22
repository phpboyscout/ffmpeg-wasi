package conformance

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// zeroAbstractAddress is where a driver given an empty AFMPEG_NATIVE_SOCKET
// connects: it copies "" into a zeroed sun_path and passes the whole struct's
// length, which Linux reads as an abstract name of 107 zero bytes. Any local
// process in the same network namespace can listen there.
var zeroAbstractAddress = "@" + strings.Repeat("\x00", 107)

// TestAnEmptySocketVariableIsRefused is ffmpeg-wasi#78. The bridge treated a
// set-but-empty AFMPEG_NATIVE_SOCKET as active while the sandbox treated it as
// plain-file mode, so whoever answered at the zero abstract address served the
// driver's media with no Landlock floor installed.
func TestAnEmptySocketVariableIsRefused(t *testing.T) {
	png, err := fixture.PNG(64, 48, 5)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			served := t.TempDir()
			if err := os.WriteFile(filepath.Join(served, "in.png"), png, 0o644); err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("unix", zeroAbstractAddress)
			if err != nil {
				t.Fatalf("listening on the zero abstract address: %v", err)
			}
			h := ipchost.Serve(served, ln)
			t.Cleanup(func() { _ = h.Close() })

			spec := `{"op":"process","inputs":[{"path":"in.png","format":"image2"}],` +
				`"filter":"[0:v]null[v]","outputs":[{"path":"out.png","map":["[v]"],"video_codec":"png"}]}`
			ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
			defer cancel()
			res := runAndCheck(t, engine.Native{Path: a.Path, Dir: t.TempDir(), Env: []string{"AFMPEG_NATIVE_SOCKET="}},
				ctx, spec, "a job with AFMPEG_NATIVE_SOCKET set but empty")

			if res.ExitCode == 0 {
				var reply struct {
					Sandbox string `json:"sandbox"`
				}
				_ = json.Unmarshal([]byte(res.Stdout), &reply)
				t.Fatalf("%s: the job ran (sandbox %q), and the host at the zero abstract address served %v; "+
					"want an empty AFMPEG_NATIVE_SOCKET refused with exit 2", a, reply.Sandbox, h.Opened())
			}
			if res.ExitCode != 2 || !strings.Contains(res.Stderr, "AFMPEG_NATIVE_SOCKET") {
				t.Errorf("%s: exited %d, want 2 with a diagnostic naming AFMPEG_NATIVE_SOCKET: %q",
					a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}
			if opened := h.Opened(); len(opened) > 0 {
				t.Errorf("%s: the driver still dialled the zero abstract address and opened %v", a, opened)
			}

			caps := runAndCheck(t, engine.Native{Path: a.Path, Dir: t.TempDir(), Env: []string{"AFMPEG_NATIVE_SOCKET="}},
				ctx, "--capabilities", "--capabilities with AFMPEG_NATIVE_SOCKET set but empty")
			if caps.ExitCode != 2 {
				t.Errorf("%s: --capabilities exited %d with the variable empty, want 2: it would report a state "+
					"for a configuration no job can run in", a, caps.ExitCode)
			}
		})
	}
}
