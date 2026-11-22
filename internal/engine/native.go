package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
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

// NativeWrapperEnv names a command line every native run is prefixed with, such
// as "valgrind --error-exitcode=99", so the whole suite can run under a checker
// without each test knowing (ffmpeg-wasi#75). It is split on whitespace.
const NativeWrapperEnv = "FFMPEG_WASI_NATIVE_WRAPPER"

// WrapperErrorExit is the exit a wrapper reports its own finding with, as
// build/memcheck.sh passes valgrind --error-exitcode. No driver exit uses it, so
// Run makes it an error: a test expecting a failure must not read it as one.
const WrapperErrorExit = 99

// NativeWrapper returns the command line NativeWrapperEnv names, or nil.
func NativeWrapper() []string {
	w := strings.Fields(os.Getenv(NativeWrapperEnv))
	if len(w) == 0 {
		return nil
	}
	return w
}

// Run executes the driver and collects its streams. As with WASM, a non-zero
// exit is an outcome rather than an error.
func (n Native) Run(ctx context.Context, args ...string) (Result, error) {
	argv := append(NativeWrapper(), n.Path)
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], args...)...)
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
		if res.ExitCode == WrapperErrorExit && NativeWrapper() != nil {
			return Result{}, fmt.Errorf("%s reported an error running %s: %s",
				NativeWrapperEnv, n.Path, strings.TrimSpace(stderr.String()))
		}
	default:
		return Result{}, err
	}

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	// Peak memory, so a test can assert on growth the output cannot show. Best
	// effort: a platform that does not report it leaves the field zero rather
	// than failing the run.
	if st := cmd.ProcessState; st != nil {
		if ru, ok := st.SysUsage().(*syscall.Rusage); ok {
			res.PeakRSSKiB = int64(ru.Maxrss) // Linux reports KiB
		}
		// A signalled process is not a reported failure, and the two must not be
		// allowed to look alike.
		if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = ws.Signal().String()
		}
	}
	return res, nil
}

// Describe names the driver.
func (n Native) Describe() string { return "native:" + n.Path }
