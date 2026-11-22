package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// Every job result says what confinement that job ran under, whatever its op
// (afmpeg spec 0047 D2, ffmpeg-wasi#73). The answer must be the one
// --capabilities gives in the same invocation mode: a result that disagreed would
// describe some other run.

// jobSandboxes runs a probe, a frames and a process job on in.png and returns
// each result's sandbox field, keyed by op. path maps a file name to the form
// the runner resolves.
func jobSandboxes(t *testing.T, r engine.Runner, path func(string) string) map[string]string {
	t.Helper()

	in := map[string]any{"path": path("in.png"), "format": "image2"}
	jobs := map[string]map[string]any{
		"probe": {"op": "probe", "inputs": []any{in}},
		"frames": {"op": "frames", "inputs": []any{in}, "codec": "png",
			"select": map[string]any{"timestamps": []any{0.0}}, "path": path("f%02d.png")},
		"process": {"op": "process", "inputs": []any{in}, "filter": "[0:v]null[v]",
			"outputs": []any{map[string]any{"path": path("out.png"), "map": []any{"[v]"}, "video_codec": "png"}}},
	}

	got := map[string]string{}
	for op, job := range jobs {
		spec, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
		res := runAndCheck(t, r, ctx, string(spec), op)
		cancel()
		if res.ExitCode != 0 {
			t.Fatalf("%s: %s exited %d: %s", r.Describe(), op, res.ExitCode, res.Stderr)
		}

		var reply struct {
			Sandbox *string `json:"sandbox"`
		}
		if err := json.Unmarshal([]byte(res.Stdout), &reply); err != nil {
			t.Fatalf("%s: parsing the %s result: %v\n%s", r.Describe(), op, err, res.Stdout)
		}
		if reply.Sandbox == nil {
			t.Errorf("%s: the %s result has no sandbox field: %s", r.Describe(), op, res.Stdout)
			continue
		}
		got[op] = *reply.Sandbox
	}
	return got
}

func wantSandboxEverywhere(t *testing.T, r engine.Runner, got map[string]string) {
	t.Helper()

	caps, err := engine.Query(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Sandbox == "" {
		t.Fatalf("%s: --capabilities reports no sandbox, so there is nothing to agree with", r.Describe())
	}
	for op, state := range got {
		if state != caps.Sandbox {
			t.Errorf("%s: the %s result says sandbox %q, --capabilities %q", r.Describe(), op, state, caps.Sandbox)
		}
	}
}

func TestEveryJobResultReportsItsSandbox(t *testing.T) {
	png, err := fixture.PNG(64, 48, 5)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := workspaceFor(t, a)
			if _, err := ws.Write("in.png", png); err != nil {
				t.Fatal(err)
			}
			r := ws.Runner()
			wantSandboxEverywhere(t, r, jobSandboxes(t, r, ws.Path))
		})
	}

	// Over the bridge is the mode that installs the floor. Whether it reads
	// "landlock" or "unavailable" depends on the kernel, and sandbox_test.go pins
	// that; here the result only has to agree with --capabilities.
	for _, a := range nativeArtifacts(t) {
		t.Run(a.String()+"/bridge", func(t *testing.T) {
			t.Parallel()

			served := t.TempDir()
			if err := os.WriteFile(filepath.Join(served, "in.png"), png, 0o644); err != nil {
				t.Fatal(err)
			}
			h, sock, err := ipchost.Listen(served, t.TempDir())
			if err != nil {
				t.Fatalf("%s: starting the host: %v", a, err)
			}
			t.Cleanup(func() { _ = h.Close() })

			r := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}
			wantSandboxEverywhere(t, r, jobSandboxes(t, r, func(name string) string { return name }))
		})
	}
}
