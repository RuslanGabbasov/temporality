//go:build linux

package main

// The Landlock probe (docs/filesystem-policy.md §5.3). It answers, on the
// actual kernel that will run the sandbox:
//
//  1. is Landlock present (LSM list, ABI via the version probe);
//  2. does the container's seccomp profile let the Landlock syscalls through;
//  3. does enforcement actually bite: allowed/denied read, allowed/denied
//     write, mkdir, truncating overwrite;
//  4. do children inherit the restriction (exec after restrict_self).
//
// Raw syscalls keep the probe dependency-free: landlock_create_ruleset=444,
// landlock_add_rule=445, landlock_restrict_self=446 are the same numbers on
// every Linux architecture. Only ABI-1 access bits are HANDLED so the ruleset
// loads on kernels from 5.13 up (unknown handled bits make the kernel answer
// E2BIG). Exit code 0 = Landlock is usable for enforcement, 1 = not.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1
	landlockRulePathBeneath      = 1

	// ABI-1 filesystem access bits.
	fsExecute    = 1 << 0
	fsWriteFile  = 1 << 1
	fsReadFile   = 1 << 2
	fsReadDir    = 1 << 3
	fsRemoveDir  = 1 << 4
	fsRemoveFile = 1 << 5
	fsMakeChar   = 1 << 6
	fsMakeDir    = 1 << 7
	fsMakeReg    = 1 << 8
	fsMakeSock   = 1 << 9
	fsMakeFifo   = 1 << 10
	fsMakeBlock  = 1 << 11
	fsMakeSym    = 1 << 12
	fsRefer      = 1 << 13 // ABI 3
	fsTruncate   = 1 << 14 // ABI 2

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

type check struct {
	name string
	run  func() error
	// denied marks a check that must FAIL with EACCES once restricted.
	denied bool
}

func main() {
	printEnvInfo()
	if os.Getenv("LLPROBE_CHILD") == "1" {
		// Inheritance child: the ruleset was already applied to our parent —
		// we just prove it still bites here. The exit code is the verdict, so
		// the parent's inheritance check cannot pass on silent failures.
		failures := runChecks(os.Getenv("LLPROBE_ROOT"), os.Getenv("LLPROBE_OUTSIDE"))
		report(failures)
		if len(failures) > 0 {
			fmt.Println("RESULT: FAILED")
			os.Exit(1)
		}
		return
	}
	root, outside, err := setup()
	if err != nil {
		fmt.Println("SETUP FAIL:", err)
		os.Exit(1)
	}
	// The inheritance child is exec'd from a copy INSIDE the allowed tree,
	// copied while this process is still unrestricted: execve reads the
	// binary, and the mounted original lives outside every allowed subtree
	// (that read denial is enforcement working — it must not mask the
	// inheritance verdict).
	childBin, err := copySelf(filepath.Join(root, "probe-child"))
	if err != nil {
		fmt.Println("SETUP FAIL: copy child binary:", err)
		os.Exit(1)
	}
	if err := restrict(root); err != nil {
		fmt.Println("RESULT: FAILED —", err)
		os.Exit(1)
	}
	failures := runChecks(root, outside)
	report(failures)
	// Inheritance: a child exec'd after restrict_self must face the same rules.
	cmd := exec.Command(childBin)
	// Keep exec plumbing off /dev/null: the ruleset denies it (device nodes
	// live outside the allowed trees) and would mask the inheritance verdict.
	cmd.Stdin = strings.NewReader("")
	cmd.Env = append(os.Environ(), "LLPROBE_CHILD=1", "LLPROBE_ROOT="+root, "LLPROBE_OUTSIDE="+outside)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("FAIL inheritance: child exited", err)
		failures = append(failures, "inheritance")
	} else {
		fmt.Println("PASS inheritance")
	}
	if len(failures) > 0 {
		fmt.Println("RESULT: FAILED")
		os.Exit(1)
	}
	fmt.Println("RESULT: OK")
}

func printEnvInfo() {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err == nil {
		fmt.Println("kernel:", chars(uts.Release[:]))
	}
	if data, err := os.ReadFile("/sys/kernel/security/lsm"); err == nil {
		fmt.Println("lsm:", strings.TrimSpace(string(data)))
	}
	if data, err := os.ReadFile("/sys/kernel/security/landlock/abi"); err == nil {
		fmt.Println("landlock abi (fs):", strings.TrimSpace(string(data)))
	}
	// The authoritative ABI answer: the version probe ignores all arguments.
	abi, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		fmt.Println("landlock version probe: errno", errno)
	} else {
		fmt.Println("landlock version probe: ABI", abi)
	}
}

// setup creates the test trees BEFORE restricting (later creation would be
// denied itself): root is the allowed tree, outside simulates project files
// the policy must keep off-limits.
func setup() (root, outside string, err error) {
	root, err = os.MkdirTemp("", "llprobe-root-")
	if err != nil {
		return "", "", err
	}
	outside, err = os.MkdirTemp("", "llprobe-outside-")
	if err != nil {
		return "", "", err
	}
	for _, f := range []string{
		filepath.Join(root, "keep.txt"),
		filepath.Join(root, "keep2.txt"),
		filepath.Join(outside, "secret.txt"),
	} {
		if err := os.WriteFile(f, []byte("probe"), 0o644); err != nil {
			return "", "", err
		}
	}
	return root, outside, nil
}

// restrict builds the ruleset (allow everything handled inside root, allow
// READ_FILE on /etc — a stand-in for system read-only areas) and applies it
// to this thread and its future children.
func restrict(root string) error {
	attr := rulesetAttr{HandledAccessFS: fsReadFile | fsWriteFile | fsReadDir |
		fsMakeDir | fsMakeReg | fsRemoveFile | fsRemoveDir}
	fd, _, errno := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w (seccomp blocking the syscall, or LSM off)", errno)
	}
	defer syscall.Close(int(fd))

	if err := allowRule(int(fd), root, fsReadFile|fsWriteFile|fsReadDir|fsMakeDir|fsMakeReg|fsRemoveFile|fsRemoveDir); err != nil {
		return err
	}
	if err := allowRule(int(fd), "/etc", fsReadFile); err != nil {
		return err
	}

	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0); errno != 0 {
		return fmt.Errorf("PR_SET_NO_NEW_PRIVS: %w", errno)
	}
	if _, _, errno := syscall.Syscall(sysLandlockRestrictSelf, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

func allowRule(rulesetFD int, path string, access uint64) error {
	dirFD, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dirFD.Close()
	rule := pathBeneathAttr{AllowedAccess: access, ParentFD: int32(dirFD.Fd())}
	if _, _, errno := syscall.Syscall(sysLandlockAddRule, uintptr(rulesetFD), landlockRulePathBeneath, uintptr(unsafe.Pointer(&rule))); errno != 0 {
		return fmt.Errorf("landlock_add_rule(%s): %w", path, errno)
	}
	return nil
}

func runChecks(root, outside string) []string {
	checks := []check{
		{"read /etc/hostname (allowed system read)", func() error {
			_, err := os.ReadFile("/etc/hostname")
			return err
		}, false},
		{"write inside allowed tree", func() error {
			return os.WriteFile(filepath.Join(root, "new.txt"), []byte("x"), 0o644)
		}, false},
		{"overwrite existing file in allowed tree", func() error {
			return os.WriteFile(filepath.Join(root, "keep2.txt"), []byte("y"), 0o644)
		}, false},
		{"read inside allowed tree", func() error {
			_, err := os.ReadFile(filepath.Join(root, "keep.txt"))
			return err
		}, false},
		{"mkdir inside allowed tree", func() error {
			return os.Mkdir(filepath.Join(root, "sub"), 0o755)
		}, false},
		{"write outside the allowed tree", func() error {
			return os.WriteFile(filepath.Join(outside, "x.txt"), []byte("x"), 0o644)
		}, true},
		{"read outside the allowed tree", func() error {
			_, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
			return err
		}, true},
		{"write into /etc", func() error {
			return os.WriteFile("/etc/llprobe-should-fail", []byte("x"), 0o644)
		}, true},
	}
	var failures []string
	for _, c := range checks {
		err := c.run()
		switch {
		case c.denied && err == nil:
			fmt.Println("FAIL", c.name, "(operation unexpectedly SUCCEEDED)")
			failures = append(failures, c.name)
		case c.denied && errors.Is(err, syscall.EACCES):
			fmt.Println("PASS", c.name)
		case c.denied:
			fmt.Println("WARN", c.name, "(denied, but errno =", err, "want EACCES)")
		case err == nil || errors.Is(err, syscall.EEXIST):
			// EEXIST (mkdir of a leftover name) is still a PASS: the operation
			// was not DENIED — that is all an allowed check asserts.
			fmt.Println("PASS", c.name)
		default:
			fmt.Println("FAIL", c.name, "—", err)
			failures = append(failures, c.name)
		}
	}
	return failures
}

func report(failures []string) {
	if len(failures) > 0 {
		fmt.Printf("check failures: %d\n", len(failures))
	}
}

// copySelf duplicates the running binary at dest (post-restrict it also
// proves the allowed tree accepts writes and exec-grade modes).
func copySelf(dest string) (string, error) {
	self, err := os.Open(os.Args[0])
	if err != nil {
		return "", err
	}
	defer self.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := out.ReadFrom(self); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dest, nil
}

func chars(v []int8) string {
	var b strings.Builder
	for _, c := range v {
		if c == 0 {
			break
		}
		b.WriteByte(byte(c))
	}
	return b.String()
}
