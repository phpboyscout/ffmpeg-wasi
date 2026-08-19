package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

// Native runs the native ELF driver as a subprocess.
//
// By default it runs in PLAIN-FILE MODE: AFMPEG_NATIVE_SOCKET is not set, so the
// driver behaves as an ordinary program and opens real paths, exactly as the ABI
// reference says it does absent that variable. That is what phases A–C and D1
// use, and it keeps them free of the IPC bridge (0036 D4).
//
// Set Env to drive the bridge instead — spec 0037 phase D2, where
// internal/ipchost serves the media over a Unix socket. The two modes exercise
// different code in src/nativeio.c, so a bug in one does not imply a bug in the
// other; which one a test used is the first thing to establish when reading a
// failure.
type Native struct {
	Path string

	// Dir, when set, is the subprocess working directory, so a job's relative
	// paths resolve inside a test's temp directory rather than the repo.
	Dir string

	// Env adds variables to the subprocess environment, on top of the parent's.
	// AFMPEG_NATIVE_SOCKET=<path> is what switches the driver onto the IPC
	// bridge; anything else is passed through untouched.
	Env []string
}

// Run executes the driver and collects its streams. As with WASM, a non-zero
// exit is an outcome rather than an error.
func (n Native) Run(ctx context.Context, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, n.Path, args...)
	cmd.Dir = n.Dir
	if len(n.Env) > 0 {
		// Append rather than replace: the driver still needs the parent's
		// environment, and a bare Env would silently drop it.
		cmd.Env = append(os.Environ(), n.Env...)
	}

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
