// Command run executes a built ffmpeg-wasi module under wazero and prints its
// output — a local smoke test and the engine of `just run`.
//
// The wazero setup it needs — the env setjmp/longjmp host functions and the
// WebAssembly feature set a real FFmpeg build requires — lives in
// internal/engine, shared with the conformance suite (spec 0036 D5). Keeping one
// definition is the point: a smoke tool and a test suite that configured the
// runtime differently would disagree about what the same module does.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

func main() {
	mount := flag.String("mount", "", "host directory to mount at the guest root / (read-write)")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: run [--mount <dir>] <module.wasm> [args...]")
		os.Exit(2)
	}

	res, err := engine.WASM{Path: args[0], Mount: *mount}.Run(context.Background(), args[1:]...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}

	fmt.Print(res.Stdout)
	fmt.Fprint(os.Stderr, res.Stderr)
	os.Exit(res.ExitCode)
}
