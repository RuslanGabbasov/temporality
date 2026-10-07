package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if !strings.Contains(string(log), "run --init --pull=never") || !strings.Contains(string(log), "rm --force temporality-sandbox-") {
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
	runner := &Docker{Image: "sandbox@sha256:abc", Memory: "1g", CPUs: "2", PIDs: 128, UID: 10001, GID: 10001, ScratchSize: "512m"}
	args := runner.arguments("/srv/work/repo", Request{Command: []string{"go", "test", "./..."}, ReadOnly: true}, "temporality-sandbox-test")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--init", "--pull=never", "--name temporality-sandbox-test", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit 128", "--memory 1g", "--memory-swap 1g", "--cpus 2", "--ulimit nofile=1024:1024", "--user 10001:10001", "/srv/work/repo:/workspace:ro", "--tmpfs /tmp:rw,noexec,nosuid,size=64m", "--tmpfs /scratch:rw,exec,nosuid,size=512m", "--env HOME=/scratch", "--env TMPDIR=/scratch"} {
		if !strings.Contains(joined, required) {
			t.Errorf("docker args missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "--privileged") {
		t.Fatalf("unexpected privileged container: %s", joined)
	}
	// A Docker built without an explicit scratch size still gets a sane default.
	defaultScratch := (&Docker{}).arguments("/w", Request{Command: []string{"ls"}, ReadOnly: true}, "n")
	if !strings.Contains(strings.Join(defaultScratch, " "), "--tmpfs /scratch:rw,exec,nosuid,size=512m") {
		t.Fatalf("missing default scratch tmpfs: %s", defaultScratch)
	}
}

func TestScratchSizeMustBeAPositiveByteSize(t *testing.T) {
	for _, valid := range []string{"", "64m", "1g", "268435456", "512M"} {
		if err := validateByteSize(valid); err != nil {
			t.Errorf("validateByteSize(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"0", "m", "5x", "-1m"} {
		if err := validateByteSize(invalid); err == nil {
			t.Errorf("validateByteSize(%q) accepted", invalid)
		}
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

// The security matrix (docs/sandbox-security-matrix.md) is pinned by these
// args-level tests: network defaults to none, credentials never ride along as
// environment, and the workspace stays the single mount.

func TestNetworkDefaultsToNoneAndBridgeIsExplicit(t *testing.T) {
	runner := &Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Memory: "1g", CPUs: "2", PIDs: 128}
	base := runner.arguments("/w", Request{Command: []string{"ls"}}, "n")
	if got := flagValue(base, "--network"); got != "none" {
		t.Fatalf("default network = %q, want none", got)
	}
	// Request-level network overrides the runner default.
	bridged := runner.arguments("/w", Request{Command: []string{"ls"}, Network: "bridge"}, "n")
	if got := flagValue(bridged, "--network"); got != "bridge" {
		t.Fatalf("request network = %q, want bridge", got)
	}
	// A runner explicitly configured with bridge keeps it when the request is silent.
	configured := (&Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Network: "bridge", Memory: "1g", CPUs: "2", PIDs: 128}).arguments("/w", Request{Command: []string{"ls"}}, "n")
	if got := flagValue(configured, "--network"); got != "bridge" {
		t.Fatalf("runner network = %q, want bridge", got)
	}
}

func TestEnvironmentCarriesNoCredentials(t *testing.T) {
	runner := &Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Memory: "1g", CPUs: "2", PIDs: 128}
	args := runner.arguments("/w", Request{Command: []string{"ls"}}, "n")
	envs := map[string]bool{}
	for i, token := range args {
		if token == "--env" && i+1 < len(args) {
			envs[args[i+1]] = true
		}
	}
	if len(envs) != 2 || !envs["HOME=/scratch"] || !envs["TMPDIR=/scratch"] {
		t.Fatalf("sandbox env must be exactly HOME/TMPDIR, got %v", envs)
	}
	// The host environment (provider keys, kernel tokens) never leaks: only
	// explicit --env values reach the container, and docker run inherits none.
	for _, token := range args {
		for _, leak := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD"} {
			if strings.Contains(token, leak) && strings.Contains(token, "=") {
				t.Fatalf("potential credential in docker args: %s", token)
			}
		}
	}
}

func TestWorkspaceIsTheOnlyVolumeMount(t *testing.T) {
	runner := &Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Memory: "1g", CPUs: "2", PIDs: 128}
	for _, request := range []Request{{Command: []string{"ls"}}, {Command: []string{"ls"}, ReadOnly: true}} {
		args := runner.arguments("/srv/work/repo", request, "n")
		mounts := 0
		for i, token := range args {
			if token == "--volume" {
				mounts++
				mount := args[i+1]
				if want := "/srv/work/repo:/workspace:" + map[bool]string{true: "ro", false: "rw"}[request.ReadOnly]; mount != want {
					t.Fatalf("workspace mount = %q, want %q", mount, want)
				}
			}
			if token == "-v" || token == "--mount" || token == "--privileged" {
				t.Fatalf("unexpected mount/privilege flag %q in %v", token, args)
			}
		}
		if mounts != 1 {
			t.Fatalf("expected exactly one volume mount, got %d in %v", mounts, args)
		}
	}
}

func TestExecuteEnforcesRequestTimeout(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \"$1\" in\nrun) exec sleep 10;;\nrm) exit 0;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Binary: binary, Memory: "1g", CPUs: "2", PIDs: 128, UID: os.Getuid(), GID: os.Getgid()}
	started := time.Now()
	_, err := runner.Execute(context.Background(), Request{Workspace: workspace, Command: []string{"sleep", "10"}, TimeoutSeconds: 1})
	if err == nil || !strings.Contains(err.Error(), "exceeded 1 second timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced, took %s", elapsed)
	}
}

func flagValue(args []string, flag string) string {
	for i, token := range args {
		if token == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(token, flag+"=") {
			return strings.TrimPrefix(token, flag+"=")
		}
	}
	return ""
}
