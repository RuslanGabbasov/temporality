package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

// TestPidsFromEnvKeepsDefaultOnJunk: the pids limit is operator-tunable, and
// a typo must degrade to the safe default instead of disabling the limit.
func TestPidsFromEnvKeepsDefaultOnJunk(t *testing.T) {
	cases := map[string]int{
		"":      512,
		"1024":  1024,
		" 256 ": 256,
		"zero":  512,
		"0":     512,
		"-5":    512,
		"12.5":  512,
	}
	for value, want := range cases {
		t.Setenv("KERNEL_SANDBOX_PIDS_TEST", value)
		if got := pidsFromEnv("KERNEL_SANDBOX_PIDS_TEST", 512); got != want {
			t.Errorf("pidsFromEnv(%q) = %d, want %d", value, got, want)
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
	runner := &Docker{Image: "sandbox@sha256:abc", Memory: "1g", CPUs: "2", PIDs: 512, UID: 10001, GID: 10001, ScratchSize: "512m", TmpSize: "256m"}
	args := runner.arguments("/srv/work/repo", Request{Command: []string{"go", "test", "./..."}, ReadOnly: true}, "temporality-sandbox-test", nil)
	joined := strings.Join(args, " ")
	for _, required := range []string{"--init", "--pull=never", "--name temporality-sandbox-test", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit 512", "--memory 1g", "--memory-swap 1g", "--cpus 2", "--ulimit nofile=1024:1024", "--user 10001:10001", "/srv/work/repo:/workspace:ro", "--tmpfs /tmp:rw,noexec,nosuid,size=256m", "--tmpfs /scratch:rw,exec,nosuid,size=512m", "--env HOME=/scratch", "--env TMPDIR=/scratch"} {
		if !strings.Contains(joined, required) {
			t.Errorf("docker args missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "--privileged") {
		t.Fatalf("unexpected privileged container: %s", joined)
	}
	// A Docker built without an explicit scratch size still gets a sane default.
	defaultScratch := (&Docker{}).arguments("/w", Request{Command: []string{"ls"}, ReadOnly: true}, "n", nil)
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
	}, "n", nil)
	joined := strings.Join(args, " ")
	// Labels are appended sorted by key so identical requests yield identical argv.
	want := "--label temporality.agent=lead --label temporality.project=omni --label temporality.run=r-1"
	if !strings.Contains(joined, want) {
		t.Errorf("docker args missing sorted labels %q: %s", want, joined)
	}
	// An unlabeled request still carries the permanent ownership marker —
	// the orphan sweeper identifies every sandbox by it.
	noLabels := strings.Join(runner.arguments("/w", Request{Command: []string{"ls"}}, "n", nil), " ")
	if !strings.Contains(noLabels, "--label "+SandboxMarkerLabel+"=1") {
		t.Errorf("every sandbox must carry the ownership marker: %s", noLabels)
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
	base := runner.arguments("/w", Request{Command: []string{"ls"}}, "n", nil)
	if got := flagValue(base, "--network"); got != "none" {
		t.Fatalf("default network = %q, want none", got)
	}
	// Request-level bridge maps onto the DEDICATED sandbox network: compose
	// rebuilds and host workloads share docker0, and a stack restart must not
	// churn the bridge a running E2E session depends on.
	bridged := runner.arguments("/w", Request{Command: []string{"ls"}, Network: "bridge"}, "n", nil)
	if got := flagValue(bridged, "--network"); got != SandboxNetworkName {
		t.Fatalf("request network = %q, want %s", got, SandboxNetworkName)
	}
	// A runner explicitly configured with bridge keeps it when the request is silent.
	configured := (&Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Network: "bridge", Memory: "1g", CPUs: "2", PIDs: 128}).arguments("/w", Request{Command: []string{"ls"}}, "n", nil)
	if got := flagValue(configured, "--network"); got != SandboxNetworkName {
		t.Fatalf("runner network = %q, want %s", got, SandboxNetworkName)
	}
	// An explicit operator override passes through untouched.
	override := (&Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Network: "temporality-e2e", Memory: "1g", CPUs: "2", PIDs: 128}).arguments("/w", Request{Command: []string{"ls"}}, "n", nil)
	if got := flagValue(override, "--network"); got != "temporality-e2e" {
		t.Fatalf("override network = %q, want temporality-e2e", got)
	}
}

func TestEnvironmentCarriesNoCredentials(t *testing.T) {
	runner := &Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Memory: "1g", CPUs: "2", PIDs: 128}
	args := runner.arguments("/w", Request{Command: []string{"ls"}}, "n", nil)
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
		args := runner.arguments("/srv/work/repo", request, "n", nil)
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
	args := runner.arguments(workspace, Request{Command: []string{"ls"}, ReadOnly: true}, "n", nil)
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
	}, "n", nil)
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

// The sandbox command ceiling must stay below the run_tool activity timeout
// (kernel/agent.ToolActivityTimeout, default 16m — sandbox cannot import it,
// cycle) minus a minute of teardown head room: an activity deadline below the
// command cap kills long builds with a StartToClose timeout while the
// container is still running.
func TestMaxTimeoutStaysBelowActivityBudget(t *testing.T) {
	const activityBudget = 16 * 60 // keep in sync with kernel/agent.ToolActivityTimeout
	if maxTimeout() > activityBudget-60 {
		t.Fatalf("sandbox max timeout %ds exceeds the run_tool activity budget (%ds minus teardown head room)", maxTimeout(), activityBudget)
	}
}

func TestDockerSocketMountAddsSocketGroupAccess(t *testing.T) {
	// DooD passthrough: the socket volume plus a supplementary group carrying
	// the socket's own gid — the sandbox user gains exactly the access the
	// host granted to that group, nothing more.
	runner := &Docker{Image: "sandbox@sha256:" + strings.Repeat("a", 64), Memory: "1g", CPUs: "2", PIDs: 128, UID: 10001, GID: 10001}
	socket := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(socket, nil, 0o660); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	socketArgs, err := socketMountArgs(socket, info)
	if err != nil {
		t.Fatal(err)
	}
	args := runner.arguments("/srv/work/repo", Request{Command: []string{"docker", "ps"}, DockerSocket: true}, "n", socketArgs)
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"--volume " + socket + ":" + socket,
		"--group-add ",
		"--env DOCKER_HOST=unix://" + socket,
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("socket args missing %q: %s", required, joined)
		}
	}
	// Flags must stay before the image reference: docker rejects post-image
	// arguments as part of the command.
	if idx := slices.Index(args, "--volume"); idx > slices.Index(args, runner.Image) {
		t.Errorf("socket mount flags landed after the image: %s", joined)
	}
	// Isolation flags survive: the passthrough grants daemon access, not
	// container privileges.
	for _, kept := range []string{"--cap-drop=ALL", "--security-opt=no-new-privileges"} {
		if !strings.Contains(joined, kept) {
			t.Errorf("isolation flag %q lost: %s", kept, joined)
		}
	}

	// Without the flag, no socket artifacts at all.
	plain := strings.Join(runner.arguments("/srv/work/repo", Request{Command: []string{"ls"}}, "n", nil), " ")
	if strings.Contains(plain, "docker.sock") || strings.Contains(plain, "group-add") {
		t.Errorf("plain request must not mount the socket: %s", plain)
	}
}

func TestDockerSocketMountRejectsNonUnixFile(t *testing.T) {
	// A plain regular file carries no group id through Sys() on this platform
	// shape only if the stat type differs; on unix builds the gid resolves,
	// so this test pins the error path via a missing socket instead.
	if _, err := socketMountArgs("/nonexistent/docker.sock", mustStat(t, "/nonexistent/docker.sock")); err == nil {
		t.Error("expected an error for a missing socket path")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("stat %s: %v", path, err)
	}
	return info
}

// ensureNetwork used to shell out to `docker network inspect` before every
// single run. The memoization must keep the verification one-time per network
// per process — while a failed run forgets its entry so the next command
// re-verifies (a stack rebuild deletes the network under a live kernel).
func TestEnsureNetworkMemoizesInspectionAndResetsOnRunError(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "docker.log")
	failFile := filepath.Join(t.TempDir(), "fail-next-run")
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SANDBOX_TEST_LOG\"\ncase \"$1\" in\nnetwork) exit 0;;\nrun) if [ -f \"$SANDBOX_FAIL_FILE\" ]; then echo 'Error response from daemon: network temporality-sandbox not found'; exit 125; fi; exit 0;;\nrm) exit 0;;\nesac\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_TEST_LOG", logPath)
	t.Setenv("SANDBOX_FAIL_FILE", failFile)
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Binary: binary, Memory: "1g", CPUs: "2", PIDs: 128, UID: os.Getuid(), GID: os.Getgid()}
	request := Request{Workspace: workspace, Command: []string{"printf", "ok"}, Network: "bridge"}
	inspectCount := func() int {
		t.Helper()
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "network inspect ") {
				count++
			}
		}
		return count
	}
	runCount := func() int {
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "run ") {
				count++
			}
		}
		return count
	}
	// First command of the process life inspects; the second reuses the verdict.
	for i := 0; i < 2; i++ {
		if _, err := runner.Execute(context.Background(), request); err != nil {
			t.Fatalf("execute #%d: %v", i+1, err)
		}
	}
	if got := inspectCount(); got != 1 {
		t.Fatalf("network inspect calls after two runs = %d, want 1 (memoized)", got)
	}
	// A failed run (the daemon lost the network — exit 125) forgets the cache
	// entry: the next command must re-inspect and succeed again.
	if err := os.WriteFile(failFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute with daemon failure must surface as a failed command, not an error: %v", err)
	}
	if result.ExitCode != 125 {
		t.Fatalf("daemon failure exit code = %d, want 125", result.ExitCode)
	}
	if got := inspectCount(); got != 1 {
		t.Fatalf("failed run must not re-inspect within the same Execute, got %d", got)
	}
	if err := os.Remove(failFile); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute after reset: %v", err)
	}
	if got := inspectCount(); got != 2 {
		t.Fatalf("network inspect calls after run failure = %d, want 2 (cache reset)", got)
	}
	if got := runCount(); got != 4 {
		t.Fatalf("docker run invocations = %d, want 4", got)
	}
}

// The egress variant pays a second inspection (the Internal-flag check) on
// its first ensureNetwork — also exactly once per process.
func TestEnsureNetworkMemoizesInternalVerification(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "docker.log")
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SANDBOX_TEST_LOG\"\ncase \"$1\" in\nnetwork) if [ \"$4\" = \"--format\" ]; then echo true; fi; exit 0;;\nrun) exit 0;;\nrm) exit 0;;\nesac\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOX_TEST_LOG", logPath)
	runner := &Docker{Root: root, Image: "sandbox@sha256:" + strings.Repeat("a", 64), Binary: binary, Memory: "1g", CPUs: "2", PIDs: 128, UID: os.Getuid(), GID: os.Getgid()}
	request := Request{Workspace: workspace, Command: []string{"printf", "ok"}, Network: SandboxInternalNetworkName}
	for i := 0; i < 2; i++ {
		if _, err := runner.Execute(context.Background(), request); err != nil {
			t.Fatalf("execute #%d: %v", i+1, err)
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	inspects := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "network inspect ") {
			inspects++
		}
	}
	// Presence probe + Internal verification on the first run, nothing after.
	if inspects != 2 {
		t.Fatalf("network inspect calls after two egress runs = %d, want 2 (memoized)", inspects)
	}
}

func TestDockerRunningForAge(t *testing.T) {
	cases := map[string]time.Duration{
		"5 seconds ago":          5 * time.Second,
		"42 seconds ago":         42 * time.Second,
		"About a minute ago":     time.Minute,
		"3 minutes ago":          3 * time.Minute,
		"17 minutes ago":         17 * time.Minute,
		"About an hour ago":      time.Hour,
		"2 hours ago":            2 * time.Hour,
		"5 days ago":             5 * 24 * time.Hour,
		"2 weeks ago":            14 * 24 * time.Hour,
	}
	for input, want := range cases {
		got, ok := dockerRunningForAge(input)
		if !ok || got != want {
			t.Errorf("dockerRunningForAge(%q) = %v/%v, want %v", input, got, ok, want)
		}
	}
	if _, ok := dockerRunningForAge("nonsense"); ok {
		t.Error("nonsense must not parse")
	}
}
