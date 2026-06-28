// Command run executes a built ffmpeg-wasi module under wazero and prints its
// output — a local smoke test and the engine of `just run`. It provides the env
// setjmp/longjmp host functions and the WebAssembly feature set a real FFmpeg
// build needs (the same the afmpeg runtime provides).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const features = api.CoreFeaturesV2 |
	experimental.CoreFeaturesExtendedConst |
	experimental.CoreFeaturesTailCall

type storeKey struct{}

type sjlj struct{ snaps map[uint32]experimental.Snapshot }

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

func main() {
	mount := flag.String("mount", "", "host directory to mount at the guest root / (read-write)")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: run [--mount <dir>] <module.wasm> [args...]")
		os.Exit(2)
	}

	ctx := context.Background()

	wasm, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCoreFeatures(features))
	defer rt.Close(ctx)

	if _, err := rt.NewHostModuleBuilder("env").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(setjmp),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, nil).Export("__wasm_setjmp").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(longjmp),
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, nil).Export("__wasm_longjmp").
		Instantiate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "env:", err)
		os.Exit(1)
	}

	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile:", err)
		os.Exit(1)
	}

	cctx := experimental.WithSnapshotter(context.WithValue(ctx, storeKey{}, &sjlj{snaps: map[uint32]experimental.Snapshot{}}))
	cfg := wazero.NewModuleConfig().WithName("").
		WithArgs(append([]string{"ffmpeg-wasi"}, args[1:]...)...).
		WithStdout(os.Stdout).WithStderr(os.Stderr)
	if *mount != "" {
		cfg = cfg.WithFSConfig(wazero.NewFSConfig().WithDirMount(*mount, "/"))
	}

	if _, err := rt.InstantiateModule(cctx, compiled, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
}
