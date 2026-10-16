package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewFromEnvRootWithoutImageIsNotARunner(t *testing.T) {
	// The compose default points KERNEL_SANDBOX_ROOT at /tmp while the image
	// stays unconfigured until onboarding: uploads, inbound channels and
	// workspace files read the root directly, so this must not be an error —
	// only the run_command runner stays inert.
	t.Setenv("KERNEL_SANDBOX_IMAGE", "")
	t.Setenv("KERNEL_SANDBOX_ROOT", t.TempDir())
	runner, err := NewFromEnv()
	if err != nil {
		t.Fatalf("root without image must not error: %v", err)
	}
	if runner != nil {
		t.Fatalf("expected no runner without an image, got %+v", runner)
	}
}

func TestNewFromEnvImageWithoutRootFails(t *testing.T) {
	t.Setenv("KERNEL_SANDBOX_IMAGE", "sandbox@sha256:"+strings.Repeat("a", 64))
	t.Setenv("KERNEL_SANDBOX_ROOT", "")
	if _, err := NewFromEnv(); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("image without root must fail with a requires-root error, got %v", err)
	}
}

func TestClampCPUsStaysWithinDaemonAvailability(t *testing.T) {
	// The daemon rejects quotas above the CPUs it can see; on a 1-CPU host
	// the default "2" bricks every run_command until the quota is clamped.
	cases := []struct {
		requested string
		available int
		want      string
	}{
		{"2", 1, "1"},
		{"2", 8, "2"},
		{"0.5", 1, "0.5"},
		{"2.50", 2, "2"},
		{" 2 ", 1, "1"},
		{"garbage", 1, "garbage"},
		{"0", 1, "0"},
		{"-1", 8, "-1"},
	}
	for _, tc := range cases {
		if got := clampCPUs(tc.requested, tc.available); got != tc.want {
			t.Errorf("clampCPUs(%q, %d) = %q, want %q", tc.requested, tc.available, got, tc.want)
		}
	}
}

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
	runner := &Docker{Image: "sandbox@sha256:abc", Memory: "1g", CPUs: "2", PIDs: 128, UID: 10001, GID: 10001, ScratchSize: "512m", TmpSize: "256m"}
	args := runner.arguments("/srv/work/repo", Request{Command: []string{"go", "test", "./..."}, ReadOnly: true}, "temporality-sandbox-test")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--init", "--pull=never", "--name temporality-sandbox-test", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit 128", "--memory 1g", "--memory-swap 1g", "--cpus 2", "--ulimit nofile=1024:1024", "--user 10001:10001", "/srv/work/repo:/workspace:ro", "--tmpfs /tmp:rw,noexec,nosuid,size=256m", "--tmpfs /scratch:rw,exec,nosuid,size=512m", "--env HOME=/scratch", "--env TMPDIR=/scratch"} {
		if !strings.Contains(joined, required) {
			t.Errorf("docker args missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "--privileged") {
		t.Fatalf("unexpected privileged container: %s", joined)
	}
	// A Docker built without an explicit scratch size still gets a sane default.
	defaultScratch := (&Docker{}).arguments("/w", Request{Command: []string{"ls"}, ReadOnly: true}, "n")
	defaultJoined := strings.Join(defaultScratch, " ")
	if !strings.Contains(defaultJoined, "--tmpfs /scratch:rw,exec,nosuid,size=512m") {
		t.Fatalf("missing default scratch tmpfs: %s", defaultScratch)
	}
	if !strings.Contains(defaultJoined, "--tmpfs /tmp:rw,noexec,nosuid,size=256m") {
		t.Fatalf("missing default /tmp tmpfs: %s", defaultScratch)
	}
}

func TestDockerArgumentsCarrySortedLabels(t *testing.T) {
	runner := &Docker{Image: "sandbox@sha256:abc"}
	args := runner.arguments("/w", Request{
		Command: []string{"ls"},
		Labels:  map[string]string{"temporality.run": "r-1", "temporality.agent": "lead", "temporality.project": "omni"},
	}, "n")
	joined := strings.Join(args, " ")
	// Labels are appended sorted by key so identical requests yield identical argv.
	want := "--label temporality.agent=lead --label temporality.project=omni --label temporality.run=r-1"
	if !strings.Contains(joined, want) {
		t.Errorf("docker args missing sorted labels %q: %s", want, joined)
	}
	noLabels := strings.Join(runner.arguments("/w", Request{Command: []string{"ls"}}, "n"), " ")
	if strings.Contains(noLabels, "--label") {
		t.Errorf("unlabeled request must not emit --label: %s", noLabels)
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
	// Note: with a configured Root a second mount appears — the per-project
	// /cache volume covered by TestProjectCacheMountsPerProjectUnderRoot. This
	// test pins the baseline: no Root, no cache, workspace stays the only mount.
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

// The per-project cache: a second host mount under the sandbox root, kept
// outside the git workspace so toolchain caches survive `git clean` and
// workspace resets. It must appear for in-root workspaces and never for
// foreign ones.
func TestProjectCacheMountsPerProjectUnderRoot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "projects", "demo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Memory: "1g", CPUs: "2", PIDs: 128}
	args := runner.arguments(workspace, Request{Command: []string{"ls"}, ReadOnly: true}, "n")
	joined := strings.Join(args, " ")
	cache := filepath.Join(root, "cache", "demo")
	for _, required := range []string{
		"--volume " + cache + ":/cache:rw",
		"--env GOCACHE=/cache/go-build",
		"--env GOMODCACHE=/cache/go-mod",
		"--env GOTMPDIR=/scratch",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("docker args missing %q: %s", required, joined)
		}
	}
	if info, err := os.Stat(filepath.Join(cache, "go-build")); err != nil || !info.IsDir() {
		t.Fatalf("cache subdirectory must exist: %v", err)
	}
	// Foreign workspaces and runners without a root get no cache mount.
	if runner.projectCacheDir(filepath.Join(t.TempDir(), "other"), nil) != "" {
		t.Fatal("workspace outside the root must not get a cache mount")
	}
	if (&Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64)}).projectCacheDir(workspace, nil) != "" {
		t.Fatal("runner without a root must not mount a cache")
	}
}

// A sandbox profile overrides the image and resources, and its cache env
// replaces the legacy Go pointers with the profile's toolchain caches
// (docs/sandbox-profiles.md §3.3).
func TestDockerArgumentsApplySandboxProfile(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "projects", "demo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Memory: "1g", CPUs: "2", PIDs: 128, ScratchSize: "512m", TmpSize: "256m"}
	profileImage := "temporality-sandbox-node@sha256:" + strings.Repeat("b", 64)
	args := runner.arguments(workspace, Request{
		Command:     []string{"node", "-v"},
		Image:       profileImage,
		Memory:      "2g",
		CPUs:        "4",
		PIDs:        256,
		ScratchSize: "1g",
		TmpSize:     "512m",
		CacheEnv:    []string{"NPM_CONFIG_CACHE=/cache/npm"},
	}, "n")
	joined := strings.Join(args, " ")

	if got := flagValue(args, "--memory"); got != "2g" {
		t.Errorf("--memory = %q, want 2g", got)
	}
	if got := flagValue(args, "--cpus"); got != "4" {
		t.Errorf("--cpus = %q, want 4", got)
	}
	if got := flagValue(args, "--pids-limit"); got != "256" {
		t.Errorf("--pids-limit = %q, want 256", got)
	}
	if !strings.Contains(joined, "--tmpfs /scratch:rw,exec,nosuid,size=1g") {
		t.Errorf("scratch override missing: %s", joined)
	}
	if !strings.Contains(joined, "--tmpfs /tmp:rw,noexec,nosuid,size=512m") {
		t.Errorf("tmp override missing: %s", joined)
	}
	if !strings.Contains(joined, "--env NPM_CONFIG_CACHE=/cache/npm") {
		t.Errorf("profile cache env missing: %s", joined)
	}
	if strings.Contains(joined, "GOCACHE") || strings.Contains(joined, "GOMODCACHE") {
		t.Errorf("legacy Go caches must yield to the profile's caches: %s", joined)
	}
	// The image is the flag tail right before the command; assert by absence of
	// the runner image and presence of the profile ref.
	if strings.Contains(joined, "sandbox@sha256:"+strings.Repeat("a", 64)) {
		t.Errorf("runner image must be replaced by the profile image: %s", joined)
	}
	if !strings.Contains(joined, profileImage+" node -v") {
		t.Errorf("profile image must precede the command: %s", joined)
	}
	// The npm cache directory is pre-created like the Go ones were.
	if info, err := os.Stat(filepath.Join(root, "cache", "demo", "npm")); err != nil || !info.IsDir() {
		t.Fatalf("profile cache subdirectory must exist: %v", err)
	}
}

// The digest rule holds for profile images exactly as for the runner image:
// a mutable tag could drift under the sandbox between two commands.
func TestExecuteRejectsTaggedProfileImage(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Binary: "/nonexistent/docker", Memory: "1g", CPUs: "2", PIDs: 128}
	_, err := runner.Execute(context.Background(), Request{Workspace: workspace, Command: []string{"ls"}, Image: "node:22-alpine"})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tagged profile image must be rejected before any docker call, got %v", err)
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
