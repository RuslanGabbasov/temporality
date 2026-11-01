//go:build !linux

package main

import (
	"fmt"

	"github.com/temporality-project/temporality/kernel/sandbox/landlock"
)

// applyAndExec on non-Linux: Landlock is a Linux facility, and the launcher's
// contract is fail-closed — it must never run the command unconfined.
func applyAndExec(rules []landlock.Rule, argv []string) error {
	return fmt.Errorf("Landlock enforcement requires Linux (this launcher runs inside the sandbox container)")
}
