package engine

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

// Workspace is a filesystem for one behavioural job: the inputs it reads, the
// outputs it writes, and the character devices the engine expects to find
// (spec 0036 D5).
//
// It exists because the two targets disagree about what a path means, and every
// behavioural test would otherwise carry that disagreement. The WASM module sees
// a mounted filesystem rooted at "/", so its specs name "/in.wav". The native
// driver in plain-file mode opens real paths against its working directory, so
// its specs name "in.wav". Ask the Workspace for a path and the spec is correct
// on both.
//
// # The devices, and how faithful they are
//
// The ABI reference lists four devices the engine expects. Here they are ordinary
// files in the mounted directory, which is enough for what phase C asserts and
// is NOT what a production host does:
//
//   - /dev/urandom, /dev/random — a fixed block of crypto/rand bytes, not an
//     endless stream. libav seeds Matroska from a few bytes, so a block suffices;
//     an engine that read past it would see EOF where a real host would not.
//   - /dev/null — an ordinary file. Writes accumulate instead of vanishing.
//   - /dev/afmpeg-progress — an ordinary file, which is why it is USEFUL here:
//     whatever the engine streams to the progress side-channel stays on disk for
//     a test to read back (spec 0032).
//
// afmpeg's internal/vfs synthesises all four properly. Importing it is what
// 0036 D4 rejects, so the gap is stated rather than closed: these tests prove the
// engine writes progress records and does not stall for want of entropy. They do
// not prove a host serving real devices behaves identically.
//
// On the NATIVE target the devices are the host's own, per the ABI reference —
// /dev/urandom really is /dev/urandom. Only the WASM target sees the files above.
type Workspace struct {
	dir string
	art Artifact
}

// devRandomBytes is the size of the synthetic entropy files. Four orders of
// magnitude more than the handful of bytes av_get_random_seed takes, so a test
// failing here means something other than running short.
const devRandomBytes = 64 << 10

// NewWorkspace prepares dir as a job filesystem for art: the devices, a writable
// /tmp (which concat jobs require on the WASM target), and nothing else.
//
// dir should be a t.TempDir(); Workspace never removes it, so a failing test
// leaves its outputs behind to be looked at.
func NewWorkspace(dir string, art Artifact) (*Workspace, error) {
	for _, sub := range []string{"dev", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("workspace: creating %s: %w", sub, err)
		}
	}

	entropy := make([]byte, devRandomBytes)
	if _, err := rand.Read(entropy); err != nil {
		return nil, fmt.Errorf("workspace: reading entropy: %w", err)
	}
	for _, dev := range []struct {
		name string
		body []byte
	}{
		{"dev/urandom", entropy},
		{"dev/random", entropy},
		{"dev/null", nil},
		{"dev/afmpeg-progress", nil},
	} {
		if err := os.WriteFile(filepath.Join(dir, dev.name), dev.body, 0o644); err != nil {
			return nil, fmt.Errorf("workspace: creating %s: %w", dev.name, err)
		}
	}

	return &Workspace{dir: dir, art: art}, nil
}

// Path renders name as this target's engine sees it. Use it for every path that
// goes into a job spec.
func (w *Workspace) Path(name string) string {
	if w.art.Target == "native" {
		return name
	}
	return "/" + name
}

// Write puts body at name and returns the path a job spec should use for it.
func (w *Workspace) Write(name string, body []byte) (string, error) {
	full := filepath.Join(w.dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("workspace: creating the directory for %s: %w", name, err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		return "", fmt.Errorf("workspace: writing %s: %w", name, err)
	}
	return w.Path(name), nil
}

// Read returns what is at name on the host side, whatever the target — the
// engine's output, or what it streamed to /dev/afmpeg-progress.
func (w *Workspace) Read(name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(w.dir, name))
	if err != nil {
		return nil, fmt.Errorf("workspace: reading back %s: %w", name, err)
	}
	return b, nil
}

// Glob lists the workspace files matching a shell pattern, as host-relative
// names. Used for the frames op, whose output count is the assertion.
func (w *Workspace) Glob(pattern string) ([]string, error) {
	hits, err := filepath.Glob(filepath.Join(w.dir, pattern))
	if err != nil {
		return nil, fmt.Errorf("workspace: globbing %s: %w", pattern, err)
	}
	for i, h := range hits {
		rel, err := filepath.Rel(w.dir, h)
		if err != nil {
			return nil, fmt.Errorf("workspace: relativising %s: %w", h, err)
		}
		hits[i] = rel
	}
	return hits, nil
}

// Runner invokes the artifact against this workspace — mounted for WASM, as the
// working directory for native.
func (w *Workspace) Runner() Runner {
	if w.art.Target == "native" {
		return Native{Path: w.art.Path, Dir: w.dir}
	}
	return WASM{Path: w.art.Path, Mount: w.dir}
}
