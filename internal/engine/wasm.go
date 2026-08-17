// Package engine invokes a built ffmpeg-wasi artifact the way a host does — one
// JSON argument in, one line of JSON on stdout, documented exit codes — so tests
// assert against the driver ABI rather than against any particular host's Go API
// (spec 0036 D1).
//
// Two targets implement the same Runner: the wasm32-wasi module under wazero and
// the native ELF as a subprocess. Both are published artifacts built from the
// same src/, and running the suite against each is what makes a component or
// behaviour missing on one of them visible.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// Features is the WebAssembly feature set a real FFmpeg build needs. It is
// shared with tools/run rather than restated there, so the smoke tool and the
// suite cannot drift into configuring the runtime differently (spec 0036 D5).
//
// It must stay in step with afmpeg's runtimeCoreFeatures (pkg/afmpeg/setjmp.go),
// which is the reference host: a module afmpeg can load and this cannot is a
// module we cannot test.
//
// ExceptionHandling is not optional for the intermediate profile. Its C++
// dependencies (harfbuzz, libass) compile exceptions to a wasm tag section, and
// without this the module does not even compile:
//
//	tag section not supported as feature "exception-handling" is disabled
//
// tools/run omitted it and so could never load an intermediate module — half the
// published wasm artifacts — while its own doc comment claimed it provided "the
// same [features] the afmpeg runtime provides". Nothing caught it because
// `just run` defaulted to the lean module. The conformance suite found it on its
// first CI run.
const Features = api.CoreFeaturesV2 |
	experimental.CoreFeaturesExtendedConst |
	experimental.CoreFeaturesTailCall |
	experimental.CoreFeaturesExceptionHandling

type storeKey struct{}

type sjlj struct {
	snaps map[uint32]experimental.Snapshot
}

// setjmp/longjmp are the host functions FFmpeg's error handling needs; libjpeg
// and friends use them, and without them the module refuses to instantiate.
func setjmp(ctx context.Context, _ api.Module, stack []uint64) {
	if s, ok := ctx.Value(storeKey{}).(*sjlj); ok {
		s.snaps[api.DecodeU32(stack[0])] = experimental.GetSnapshotter(ctx).Snapshot()
	}
}

func longjmp(ctx context.Context, _ api.Module, stack []uint64) {
	if s, ok := ctx.Value(storeKey{}).(*sjlj); ok {
		if snap, ok := s.snaps[api.DecodeU32(stack[0])]; ok {
			snap.Restore(stack[1:])
		}
	}
}

// Result is one invocation's outcome, mirroring the ABI reference: stdout
// carries the result, stderr a human-readable error, and the exit code says
// which kind of failure it was.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner invokes an artifact with the given arguments.
type Runner interface {
	// Run passes args as argv[1:]. A non-zero exit is returned in Result, not as
	// an error — the exit code is part of the contract under test, so treating it
	// as a Go error would make the interesting cases hard to assert.
	Run(ctx context.Context, args ...string) (Result, error)

	// Describe names the artifact, for skip and failure messages.
	Describe() string
}

// WASM runs a .wasm module under wazero.
type WASM struct {
	Path string

	// Mount, when set, is mounted at the guest root. Media I/O on this target
	// goes through the mounted filesystem, never the host's real disk.
	Mount string
}

// compilationCache shares wazero's compiled form of a module across every
// invocation in the process.
//
// It changes no semantics — each Run below still builds its own runtime and its
// own instance, so the one-shot property is intact — but it moves compilation from
// once per invocation to once per module. Phase A invoked each artifact twice and
// did not care; phase B invokes it around forty times, and recompiling a module of
// 5–13 MB every time was the dominant cost of the whole suite.
//
// In-memory and never closed, which is what a test binary wants: the process is
// short-lived and the cache is the reason it is short-lived.
var compilationCache = wazero.NewCompilationCache()

// Run compiles and instantiates the module per invocation. Instantiation per call
// is deliberate: the engine is a one-shot program that exits, so reusing an
// instance across calls would leak state between assertions. Compilation is shared
// through compilationCache, which is not the same thing.
func (w WASM) Run(ctx context.Context, args ...string) (Result, error) {
	wasm, err := os.ReadFile(w.Path)
	if err != nil {
		return Result{}, fmt.Errorf("read module: %w", err)
	}

	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCoreFeatures(Features).
		WithCompilationCache(compilationCache))
	defer rt.Close(ctx)

	if _, err := rt.NewHostModuleBuilder("env").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(setjmp),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, nil).Export("__wasm_setjmp").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(longjmp),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, nil).Export("__wasm_longjmp").
		Instantiate(ctx); err != nil {
		return Result{}, fmt.Errorf("instantiate env: %w", err)
	}

	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return Result{}, fmt.Errorf("compile: %w", err)
	}

	var stdout, stderr bytes.Buffer
	cctx := experimental.WithSnapshotter(context.WithValue(ctx, storeKey{}, &sjlj{snaps: map[uint32]experimental.Snapshot{}}))
	cfg := wazero.NewModuleConfig().WithName("").
		WithArgs(append([]string{"ffmpeg-wasi"}, args...)...).
		WithStdout(&stdout).WithStderr(&stderr)
	if w.Mount != "" {
		cfg = cfg.WithFSConfig(wazero.NewFSConfig().WithDirMount(w.Mount, "/"))
	}

	res := Result{}
	_, err = rt.InstantiateModule(cctx, compiled, cfg)
	// A guest that calls exit(N) surfaces as sys.ExitError, which is an ordinary
	// outcome here rather than a failure — exit codes 1/2/3 are the contract.
	var exitErr *sys.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = int(exitErr.ExitCode())
	default:
		return Result{}, fmt.Errorf("run: %w", err)
	}

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	return res, nil
}

// Describe names the module.
func (w WASM) Describe() string { return "wasm:" + w.Path }
