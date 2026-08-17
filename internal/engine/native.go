package engine

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Native runs the native ELF driver as a subprocess.
//
// It runs in PLAIN-FILE MODE: AFMPEG_NATIVE_SOCKET is deliberately not set, so
// the driver behaves as an ordinary program and opens real paths, exactly as the
// ABI reference says it does absent that variable. That keeps every test in this
// spec's scope free of the AFMPEG_NATIVE_SOCKET IPC bridge, whose host
// implementation is deferred to spec 0037 (0036 D4).
//
// The consequence to keep in mind when reading a failure: these tests exercise
// the engine, not the bridge. A bug that only manifests over IPC will not appear
// here.
type Native struct {
	Path string

	// Dir, when set, is the subprocess working directory, so a job's relative
	// paths resolve inside a test's temp directory rather than the repo.
	Dir string
}

// Run executes the driver and collects its streams. As with WASM, a non-zero
// exit is an outcome rather than an error.
func (n Native) Run(ctx context.Context, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, n.Path, args...)
	cmd.Dir = n.Dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	res := Result{}
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return Result{}, err
	}

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	return res, nil
}

// Describe names the driver.
func (n Native) Describe() string { return "native:" + n.Path }
