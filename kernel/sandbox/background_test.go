package sandbox

import (
	"strings"
	"testing"
)

// The background wrapper protocol (docs/agent-async-operations.md): the shell
// writes its own pid, runs the ORIGINAL argv via "$@" (no re-parsing), then
// records the exit code — kill needs the pid, attach-after-restart needs the
// code, and output keeps streaming through the normal docker exec pipe.
func TestOpShellWrapper(t *testing.T) {
	argv := OpShellWrapper("op-123", []string{"bash", "-lc", "yarn install"})
	if argv[0] != "/bin/sh" || argv[1] != "-c" {
		t.Fatalf("wrapper must run via /bin/sh -c, got %v", argv[:2])
	}
	script := argv[2]
	if !strings.Contains(script, "echo $$ > /scratch/.ops/op-123.pid") {
		t.Fatalf("wrapper must record the pid, got %q", script)
	}
	if !strings.Contains(script, `"$@"`) {
		t.Fatalf("wrapper must invoke the command via \"$@\", got %q", script)
	}
	if !strings.Contains(script, "echo $code > /scratch/.ops/op-123.code") {
		t.Fatalf("wrapper must record the exit code, got %q", script)
	}
	// The original argv rides as the wrapper's parameters.
	if len(argv) < 5 || argv[3] != "sh" || argv[4] != "bash" || argv[6] != "yarn install" {
		t.Fatalf("original argv must trail the wrapper (arg0=sh), got %v", argv)
	}
}

func TestOpMarkerAndTokenSanitize(t *testing.T) {
	if got := OpMarkerEnv("op-1"); got != "__TEMPORALITY_OP=op-1" {
		t.Fatalf("marker = %q", got)
	}
	if got := sanitizeOpToken("../../etc/passwd"); strings.ContainsAny(got, "/.") {
		t.Fatalf("token must be path-safe, got %q", got)
	}
}
