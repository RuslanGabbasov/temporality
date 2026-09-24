package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteCapturesCommandAndAlwaysRemovesContainer(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "docker.log")
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SANDBOX_TEST_LOG\"\ncase \"$1\" in\nrun) echo command-output; echo command-error >&2; exit 7;;\nrm) exit 0;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_TEST_LOG", logPath)
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Binary: binary, Memory: "1g", CPUs: "2", PIDs: 128, UID: os.Getuid(), GID: os.Getgid()}
	result, err := runner.Execute(context.Background(), Request{Workspace: workspace, Command: []string{"printf", "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || !strings.Contains(result.Output, "command-output") || !strings.Contains(result.Output, "command-error") {
		t.Fatalf("unexpected command result: %#v", result)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "run --rm --pull=never") || !strings.Contains(string(log), "rm --force temporality-sandbox-") {
		t.Fatalf("expected run and forced cleanup calls, got:\n%s", log)
	}
}

func TestWorkspaceMustStayWithinConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	runner := &Docker{Root: root, Image: "sandbox:test", PIDs: 128}
	if _, err := runner.ResolveWorkspace(filepath.Join(root, "repo")); err != nil {
		t.Fatalf("nested workspace rejected: %v", err)
	}
	if _, err := runner.ResolveWorkspace(outside); err == nil {
		t.Fatal("outside workspace accepted")
	}
	if _, err := runner.ResolveWorkspace(filepath.Join(root, "escape")); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestDockerArgumentsEnforceIsolation(t *testing.T) {
	runner := &Docker{Image: "sandbox@sha256:abc", Memory: "1g", CPUs: "2", PIDs: 128, UID: 10001, GID: 10001}
	args := runner.arguments("/srv/work/repo", Request{Command: []string{"go", "test", "./..."}, ReadOnly: true}, "temporality-sandbox-test")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--pull=never", "--name temporality-sandbox-test", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit 128", "--memory 1g", "--cpus 2", "--user 10001:10001", "/srv/work/repo:/workspace:ro"} {
		if !strings.Contains(joined, required) {
			t.Errorf("docker args missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "--privileged") {
		t.Fatalf("unexpected privileged container: %s", joined)
	}
}

func TestSandboxImageMustUseSHA256Digest(t *testing.T) {
	if err := validateImage("registry.example/team/tooling@sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := validateImage("registry.example/team/tooling:latest"); err == nil {
		t.Fatal("mutable image tag accepted")
	}
}

func TestContainerNamesAreUniqueAndStableFormat(t *testing.T) {
	first, err := containerName()
	if err != nil {
		t.Fatal(err)
	}
	second, err := containerName()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "temporality-sandbox-") {
		t.Fatalf("unexpected container names %q, %q", first, second)
	}
}

func TestValidateCommandBoundsArguments(t *testing.T) {
	if err := validateCommand([]string{"sh", "-c", "echo ok"}); err != nil {
		t.Fatal(err)
	}
	if err := validateCommand(nil); err == nil {
		t.Fatal("empty command accepted")
	}
	if err := validateCommand([]string{"sh", strings.Repeat("x", maxArgBytes+1)}); err == nil {
		t.Fatal("oversized argument accepted")
	}
}

func TestOutputBufferTruncatesWithoutBlockingWriter(t *testing.T) {
	var output limitedBuffer
	written, err := output.Write([]byte(strings.Repeat("z", maxOutputBytes+1)))
	if err != nil || written != maxOutputBytes+1 {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	if output.Len() != maxOutputBytes || !output.truncated {
		t.Fatalf("buffer len=%d truncated=%v", output.Len(), output.truncated)
	}
}
