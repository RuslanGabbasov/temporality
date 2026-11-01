//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/temporality-project/temporality/kernel/sandbox/landlock"
)

// Raw Landlock syscalls (uniform numbers on every Linux architecture);
// the probe (scripts/landlock-probe) validated the contract on the target
// kernels — see docs/filesystem-policy-implementation.md §4.
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1
	landlockRulePathBeneath      = 1

	prSetNoNewPrivs = 38
)

type rulesetAttr struct {
	HandledAccessFS uint64
}

type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFD      int32
	_             int32 // zero padding the kernel validates
}

// applyAndExec installs the ruleset on this process (and its future
// children), then replaces itself with the command. Any failure returns an
// error — the caller refuses to run (fail-closed, §5.3.6).
func applyAndExec(rules []landlock.Rule, argv []string) error {
	handled, err := handledAccess()
	if err != nil {
		return err
	}
	attr := rulesetAttr{HandledAccessFS: uint64(handled)}
	fd, _, errno := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w (kernel without Landlock, LSM disabled, or seccomp blocked the syscall)", errno)
	}
	defer syscall.Close(int(fd))
	for _, rule := range rules {
		if err := addRule(int(fd), rule, handled); err != nil {
			return err
		}
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0); errno != 0 {
		return fmt.Errorf("PR_SET_NO_NEW_PRIVS: %w", errno)
	}
	if _, _, errno := syscall.Syscall(sysLandlockRestrictSelf, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("command %q not found: %w", argv[0], err)
	}
	return syscall.Exec(path, argv, os.Environ())
}

// handledAccess computes the access bits this kernel understands: unknown
// bits in the ruleset attr make it answer E2BIG, so TRUNCATE (ABI 2) and
// REFER (ABI 3) join only when the kernel reports them. Execute is never
// handled — the policy model has no execute dimension and tools must run.
func handledAccess() (landlock.Access, error) {
	abi, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0, fmt.Errorf("landlock ABI probe: %w", errno)
	}
	handled := landlock.ReadFile | landlock.WriteFile | landlock.ReadDir |
		landlock.MakeDir | landlock.MakeReg | landlock.MakeChar | landlock.MakeFifo |
		landlock.MakeSock | landlock.MakeBlock | landlock.MakeSym |
		landlock.RemoveFile | landlock.RemoveDir
	if abi >= 2 {
		handled |= landlock.Truncate
	}
	if abi >= 3 {
		handled |= landlock.Refer
	}
	return handled, nil
}

func addRule(rulesetFD int, rule landlock.Rule, handled landlock.Access) error {
	dirFD, err := os.Open(rule.Path)
	if err != nil {
		// The tree vanished between compile and apply — nothing to protect
		// there anymore; a vanished technical dir is normal.
		return nil
	}
	defer dirFD.Close()
	attr := pathBeneathAttr{AllowedAccess: uint64(rule.Access & handled), ParentFD: int32(dirFD.Fd())}
	if _, _, errno := syscall.Syscall(sysLandlockAddRule, uintptr(rulesetFD), landlockRulePathBeneath, uintptr(unsafe.Pointer(&attr))); errno != 0 {
		return fmt.Errorf("landlock_add_rule(%s): %w", rule.Path, errno)
	}
	return nil
}
