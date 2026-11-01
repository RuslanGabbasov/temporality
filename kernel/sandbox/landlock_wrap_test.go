package sandbox

import (
	"strings"
	"testing"
)

// The Landlock wrap (docs/filesystem-policy.md §5.3): a request with a
// restricting policy prefixes the launcher and carries the snapshot; a
// request without one keeps the pre-filesystem argv byte-for-byte.
func TestRequestLandlockWrap(t *testing.T) {
	base := Request{Command: []string{"sh", "-c", "make test"}}

	if base.landlock() {
		t.Fatal("a request without a policy must not wrap")
	}
	if got := base.wrappedCommand(); len(got) != 3 || got[0] != "sh" {
		t.Fatalf("unrestricted command = %v, want untouched", got)
	}
	if got := base.commandEnv(); len(got) != 0 {
		t.Fatalf("unrestricted env = %v, want untouched", got)
	}

	unrestricted := base
	unrestricted.Filesystem = &FilesystemPolicy{}
	if unrestricted.landlock() {
		t.Fatal("an empty snapshot is unrestricted and must not wrap")
	}

	restricted := base
	restricted.Filesystem = &FilesystemPolicy{Write: [][]string{{"src/**"}}}
	restricted.PolicyVersion = "fs-abc"
	restricted.Env = []string{"SECRET_TOKEN=x"}
	if !restricted.landlock() {
		t.Fatal("a restricting snapshot must wrap")
	}
	cmd := restricted.wrappedCommand()
	if len(cmd) != 5 || cmd[0] != landlockContainerPath || cmd[1] != "--" || cmd[2] != "sh" {
		t.Fatalf("wrapped command = %v", cmd)
	}
	env := restricted.commandEnv()
	if len(env) != 3 || env[0] != "SECRET_TOKEN=x" {
		t.Fatalf("wrapped env = %v", env)
	}
	if !strings.HasPrefix(env[1], "TEMPORALITY_FS_POLICY=") || !strings.Contains(env[1], `"src/**"`) {
		t.Fatalf("policy env = %q", env[1])
	}
	if env[2] != "TEMPORALITY_FS_POLICY_VERSION=fs-abc" {
		t.Fatalf("version env = %q", env[2])
	}
	if len(base.Command) != 3 {
		t.Fatal("wrapping must not mutate the original command slice")
	}
}
