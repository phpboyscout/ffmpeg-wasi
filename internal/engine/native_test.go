package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAnUnsetOrBlankWrapperIsNil(t *testing.T) {
	for _, v := range []string{"", "  "} {
		t.Setenv(NativeWrapperEnv, v)
		if w := NativeWrapper(); w != nil {
			t.Errorf("%s=%q: NativeWrapper() = %#v, want nil, or jobTimeout runs at the wrapper's twenty minutes",
				NativeWrapperEnv, v, w)
		}
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAWrapperFindingIsAnErrorNotAFailedJob(t *testing.T) {
	driver := script(t, "exit 2")

	t.Setenv(NativeWrapperEnv, script(t, `echo "==1== Conditional jump" >&2; exit 99`))
	if res, err := (Native{Path: driver}).Run(context.Background(), "{}"); err == nil {
		t.Errorf("a wrapper exiting %d came back as a job exiting %d, which a failure-path test accepts",
			WrapperErrorExit, res.ExitCode)
	}

	t.Setenv(NativeWrapperEnv, script(t, `exec "$@"`))
	res, err := Native{Path: driver}.Run(context.Background(), "{}")
	if err != nil || res.ExitCode != 2 {
		t.Errorf("through a passthrough wrapper: exit %d, err %v; want the driver's own exit 2", res.ExitCode, err)
	}
}
