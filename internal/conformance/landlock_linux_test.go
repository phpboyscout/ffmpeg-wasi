package conformance

import "syscall"

// landlockCreateRuleset is the same number on every Linux architecture, being
// newer than the unified syscall table.
const (
	landlockCreateRuleset        = 444
	landlockCreateRulesetVersion = 1 << 0
)

// landlockABI is the Landlock ABI version this process may use, asked the way
// src/sandbox.c asks, so a seccomp profile that refuses the driver refuses this.
func landlockABI() (int, error) {
	abi, _, errno := syscall.Syscall(landlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0, errno
	}
	return int(abi), nil
}
