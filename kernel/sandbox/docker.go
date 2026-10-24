// Package sandbox runs model-authored commands in a constrained Docker
// container. The caller supplies one workspace path below an administrator-
// configured root; the host is never mounted wholesale.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxCommandArgs = 128
	// maxArgBytes keeps any single argv element within exec() limits while
	// leaving room for substantial inline scripts (sed/awk programs, small
	// heredocs). File payloads belong to the native write_file tool, which has
	// no shell argument cap.
	maxArgBytes    = 32 << 10
	maxTotalArgs   = 128 << 10
	maxOutputBytes = 64 << 10
	// defaultTimeout covers the calls where the model did not pass
	// timeout_sec: 60s killed legitimate builds and e2e suites mid-run («
	// sandbox command exceeded 60 second timeout»), while the ceiling stays
	// the real limit for anything longer.
	defaultTimeout = 300
)

// maxTimeoutSeconds caps one sandbox command. 15 minutes: e2e suites and
// builds legitimately run long (the model passes timeout_sec per call).
// Overridable per deployment; the run_tool activity timeout must stay above
// it (see kernel/agent.ToolActivityTimeout).
var maxTimeoutSeconds = func() int {
	const fallback = 900
	raw := strings.TrimSpace(os.Getenv("KERNEL_TOOL_MAX_SECONDS"))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < defaultTimeout {
		return fallback
	}
	return value
}()

func maxTimeout() int { return maxTimeoutSeconds }

type Request struct {
	Workspace      string
	Command        []string
	TimeoutSeconds int
	ReadOnly       bool
	Network        string // "" (use Docker default), "none", "bridge"
	// Env carries injected environment variables (KEY=VALUE) for this command —
	// e.g. agent-granted secrets resolved at the execution boundary. They pass
	// through exec argv (no shell), but keys still get validated to keep docker
	// CLI argument parsing unambiguous.
	Env []string
	// Sandbox-profile fields (docs/sandbox-profiles.md §3.3): empty values
	// fall back to the runner's installation defaults, so requests without a
	// profile behave exactly as before the registry existed.
	Image       string // digest-pinned override of the runner image
	Memory      string // --memory override
	CPUs        string // --cpus override
	PIDs        int    // --pids-limit override
	ScratchSize string // /scratch tmpfs size override
	TmpSize     string // /tmp tmpfs size override
	// CacheEnv replaces the legacy Go cache env set with the profile's cache
	// pointers (KEY=VALUE against /cache). Empty keeps GOCACHE/GOMODCACHE.
	CacheEnv []string
	// ScratchKey makes /scratch run-scoped instead of per-command tmpfs: a
	// host directory (under the sandbox root) bind-mounted read-write, so
	// files survive between run_command calls of one run and are swept by age
	// afterwards. Sanitized by the caller; empty keeps the ephemeral tmpfs.
	ScratchKey string
	// DockerSocket mounts the host daemon socket into the sandbox
	// (Docker-out-of-Docker, docs/sandbox-profiles.md §5.3). Opt-in per
	// custom profile: the socket is host-root-equivalent, so only profiles an
	// operator explicitly flagged get it.
	DockerSocket bool
	// Labels tag the container for observability (docker --label): the health
	// endpoint lists running sandboxes with their run/agent/actor so the
	// dashboard can show who consumes the host. Keys are used verbatim.
	Labels map[string]string
	// OnOutput, when set, receives stdout/stderr chunks as the command runs —
	// the live console stream of the run_command special scenario
	// (docs/specs/trassirovka-vkladka-zapuski/v2-consolidated.md §6). The two
	// streams arrive separately, matching the runner's own split buffers.
	// Chunks are ephemeral: they never enter the journal, the database or
	// files (§11.1 — persistent storage of output is deferred). Nil keeps the
	// classic wait-for-completion behavior.
	OnOutput func(stream string, chunk []byte)
}

type Result struct {
	Output   string        `json:"output"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
}

type Runner interface {
	SandboxRoot() string
	ResolveWorkspace(path string) (string, error)
	Execute(context.Context, Request) (Result, error)
	// RunScratchDir maps a "<project>/<run>" scratch key onto the host
	// directory the runner mounts at /scratch (run-scoped scratch).
	RunScratchDir(key string) string
}

type Docker struct {
	Root        string
	Image       string
	Binary      string
	Memory      string
	CPUs        string
	PIDs        int
	UID         int
	GID         int
	ScratchSize string
	TmpSize     string
	Network     string // "none" (default) or "bridge" for network access
}

func NewFromEnv() (*Docker, error) {
	image := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_IMAGE"))
	root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
	if image == "" {
		// A configured root without an image is legitimate: file uploads,
		// inbound channels and workspace files read KERNEL_SANDBOX_ROOT directly
		// and stay functional; only the run_command runner stays inert until an
		// image is configured (env or first-run onboarding).
		return nil, nil
	}
	if root == "" {
		return nil, errors.New("KERNEL_SANDBOX_IMAGE requires KERNEL_SANDBOX_ROOT")
	}
	if err := validateByteSize(strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_SCRATCH_SIZE"))); err != nil {
		return nil, err
	}
	if err := validateByteSize(strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_SCRATCH_SIZE"))); err != nil {
		return nil, err
	}
	// The root is created when missing: an operator pinning the path in env
	// states *where* the sandbox lives, not that they pre-created it. MkdirAll
	// is idempotent, and a typo still fails loudly one line below.
	return newDocker(image, root, true)
}

// New builds a runner from an explicit image and root (first-run onboarding
// bootstraps the image reference from workspace_setting.sandbox_image and the
// default root). Unlike the env path it creates the root directory: the
// default ./.sandbox is only correct if the kernel can materialize it.
func New(image, root string) (*Docker, error) {
	return newDocker(image, root, true)
}

func newDocker(image, root string, createRoot bool) (*Docker, error) {
	if err := validateImage(image); err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve KERNEL_SANDBOX_ROOT: %w", err)
	}
	if createRoot {
		if err := os.MkdirAll(rootAbs, 0o777); err != nil {
			return nil, fmt.Errorf("create KERNEL_SANDBOX_ROOT: %w", err)
		}
	}
	resolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve KERNEL_SANDBOX_ROOT: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return nil, errors.New("KERNEL_SANDBOX_ROOT must be an existing directory")
	}
	return &Docker{Root: resolved, Image: image, Binary: env("KERNEL_DOCKER_BINARY", "docker"), Memory: env("KERNEL_SANDBOX_MEMORY", "1g"), CPUs: clampCPUs(env("KERNEL_SANDBOX_CPUS", "2"), runtime.NumCPU()), PIDs: pidsFromEnv("KERNEL_SANDBOX_PIDS", 512), UID: os.Getuid(), GID: os.Getgid(), ScratchSize: env("KERNEL_SANDBOX_SCRATCH_SIZE", "512m"), TmpSize: env("KERNEL_SANDBOX_TMP_SIZE", "256m")}, nil
}

// pidsFromEnv parses the --pids-limit override; a missing, malformed or
// non-positive value keeps the default. 512 processes fit real development
// work (a full `go test ./...` fans out compilers and test binaries by the
// dozen); the old hardcoded 128 killed exactly that.
func pidsFromEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	pids, err := strconv.Atoi(value)
	if err != nil || pids <= 0 {
		return fallback
	}
	return pids
}

// clampCPUs keeps the --cpus quota inside what the docker daemon accepts.
// The daemon rejects any quota above the CPUs it can see ("range of CPUs is
// from 0.01 to N.00"), so a default or configured "2" on a 1-CPU host bricks
// every run_command with a docker error. Values that do not parse stay
// untouched — docker must surface that misconfiguration, not us.
func clampCPUs(requested string, available int) string {
	quota, err := strconv.ParseFloat(strings.TrimSpace(requested), 64)
	if err != nil || quota <= 0 || quota <= float64(available) {
		return requested
	}
	return strconv.FormatFloat(float64(available), 'f', -1, 64)
}

func validateImage(image string) error {
	marker := strings.LastIndex(image, "@sha256:")
	if marker <= 0 || len(image)-marker-len("@sha256:") != 64 {
		return errors.New("KERNEL_SANDBOX_IMAGE must be pinned by a sha256 digest")
	}
	for _, char := range image[marker+len("@sha256:"):] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return errors.New("KERNEL_SANDBOX_IMAGE has an invalid sha256 digest")
		}
	}
	return nil
}

// validateByteSize accepts docker tmpfs size values (plain bytes or a k/m/g
// unit) so misconfiguration fails at startup instead of mid-run.
func validateByteSize(value string) error {
	if value == "" {
		return nil
	}
	if value == "0" {
		return errors.New("scratch size must be positive")
	}
	digits := strings.TrimRight(value, "bBkKmMgG")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return fmt.Errorf("size %q must be bytes or a number with a k/m/g unit, e.g. 512m", value)
	}
	return nil
}

// SandboxRoot returns the configured sandbox root directory.
func (d *Docker) SandboxRoot() string {
	if d == nil {
		return ""
	}
	return d.Root
}

func (d *Docker) ResolveWorkspace(path string) (string, error) {
	if d == nil {
		return "", errors.New("sandbox is not configured")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("workspace path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	root, err := filepath.EvalSymlinks(d.Root)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace must be an existing directory")
	}
	if strings.ContainsAny(resolved, ":,") {
		return "", errors.New("workspace path cannot contain ':' or ','")
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("workspace must be within KERNEL_SANDBOX_ROOT")
	}
	return resolved, nil
}

func (d *Docker) Execute(parent context.Context, request Request) (result Result, runErr error) {
	if d == nil {
		return Result{}, errors.New("sandbox is not configured")
	}
	if err := validateCommand(request.Command); err != nil {
		return Result{}, err
	}
	if err := validateEnv(request.Env); err != nil {
		return Result{}, err
	}
	// A profile image must obey the same digest rule as the runner image
	// (docs/sandbox-security-matrix.md): a mutable tag could drift under the
	// sandbox between two commands of one run.
	if request.Image != "" {
		if err := validateImage(request.Image); err != nil {
			return Result{}, fmt.Errorf("sandbox profile image: %w", err)
		}
	}
	if err := validateEnv(request.CacheEnv); err != nil {
		return Result{}, fmt.Errorf("sandbox cache env: %w", err)
	}
	for _, size := range []struct{ field, value string }{
		{"memory", request.Memory}, {"cpus", request.CPUs}, {"scratch_size", request.ScratchSize}, {"tmp_size", request.TmpSize},
	} {
		if err := validateByteSize(size.value); err != nil {
			return Result{}, fmt.Errorf("sandbox profile %s: %w", size.field, err)
		}
	}
	workspace, err := d.ResolveWorkspace(request.Workspace)
	if err != nil {
		return Result{}, err
	}
	// Resolved up front so a broken socket request fails the command with a
	// clear message instead of a baffling docker-run mount error.
	var socketArgs []string
	if request.DockerSocket {
		info, statErr := os.Stat(dockerSocketPath)
		if statErr != nil {
			return Result{}, fmt.Errorf("docker socket mount requested but %s is not accessible: %w", dockerSocketPath, statErr)
		}
		socketArgs, err = socketMountArgs(dockerSocketPath, info)
		if err != nil {
			return Result{}, err
		}
	}
	timeout := request.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout() {
		timeout = maxTimeout()
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	name, err := containerName()
	if err != nil {
		return Result{}, err
	}
	args := d.arguments(workspace, request, name, socketArgs)
	// Run-scoped /scratch: the host directory must exist before the bind mount
	// (docker would create it root-owned otherwise, and the sandbox user could
	// not write into it).
	if request.ScratchKey != "" {
		scratchDir := d.runScratchDir(request.ScratchKey)
		if err := os.MkdirAll(scratchDir, 0o777); err != nil {
			return Result{}, fmt.Errorf("run scratch directory: %w", err)
		}
	}
	if err := d.ensureNetwork(d.effectiveNetwork(request)); err != nil {
		return Result{}, err
	}
	defer func() {
		if cleanupErr := d.removeContainer(name); cleanupErr != nil {
			runErr = errors.Join(runErr, cleanupErr)
		}
	}()
	started := time.Now()
	var stdout, stderr limitedBuffer
	stdout.onChunk, stderr.onChunk = request.OnOutput, request.OnOutput
	stdout.stream, stderr.stream = "stdout", "stderr"
	cmd := exec.CommandContext(ctx, d.Binary, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	result = Result{Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}
	if stdout.truncated || stderr.truncated {
		output += "\n[output truncated by Agent Kernel]"
	}
	result.Output = output
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("sandbox command exceeded %d second timeout", timeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return result, nil
		}
		return result, fmt.Errorf("start sandbox: %w", err)
	}
	return result, nil
}

// SandboxNetworkName is the dedicated bridge for network-needing sandboxes:
// isolated from compose networks and host workloads, stable across stack
// rebuilds. KERNEL_SANDBOX_NETWORK overrides the mapping entirely.
const SandboxNetworkName = "temporality-sandbox"

// SandboxInternalNetworkName is the egress-controlled variant (docs/agent-
// behavior-monitoring.md §7): created with docker --internal, so containers
// on it have no route to the internet or the LAN at all — the only way out
// is the egress proxy container attached to the same network. The activities
// layer picks this network when the egress stack is deployed.
const SandboxInternalNetworkName = "temporality-sandbox-internal"

// dockerSocketPath is the daemon socket both inside the kernel container and
// on the host: sandbox bind mounts need the path to mean the same thing in
// both namespaces (the same discipline as the sandbox root mount).
const dockerSocketPath = "/var/run/docker.sock"

// socketMountArgs renders the Docker-out-of-Docker passthrough: the socket
// volume plus a supplementary group carrying the socket's own group id, so
// the sandbox user gains exactly the access the host granted to that group —
// nothing more. DOCKER_HOST is pinned so an installed CLI talks to the
// passthrough even if an operator image carries a daemon default.
func socketMountArgs(path string, info os.FileInfo) ([]string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("docker socket mount requested but %s has no unix group id", path)
	}
	return []string{
		"--volume", path + ":" + path,
		"--group-add", strconv.FormatUint(uint64(stat.Gid), 10),
		"--env", "DOCKER_HOST=unix://" + path,
	}, nil
}

// ensureNetwork idempotently materializes the dedicated sandbox network
// before a run that needs it: an inspect hit is a no-op, a create race
// («already exists») is a win. Only the dedicated name triggers this —
// «none» and explicit operator overrides pass through untouched. A real
// failure fails the run loudly: a missing network would otherwise surface
// as a baffling per-command network error.
func (d *Docker) ensureNetwork(network string) error {
	if network != SandboxNetworkName && network != SandboxInternalNetworkName {
		return nil
	}
	inspect := exec.Command(d.Binary, "network", "inspect", network)
	if err := inspect.Run(); err != nil {
		// Absent (or broken) — create. The internal variant MUST be --internal:
		// the whole egress design rests on the sandbox network having no route
		// to the internet or the LAN.
		createArgs := []string{"network", "create"}
		if network == SandboxInternalNetworkName {
			createArgs = append(createArgs, "--internal")
		}
		create := exec.Command(d.Binary, append(createArgs, network)...)
		if out, err := create.CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
			if len(out) > 300 {
				out = out[:300]
			}
			return fmt.Errorf("create sandbox network %q: %s", network, string(out))
		}
		return nil
	}
	// Exists: for the internal variant verify it really is internal — an
	// older deployment created it as a plain bridge (internet-routed), which
	// would silently bypass the egress proxy. Recreate once; sandboxes are
	// ephemeral so there is nothing to migrate.
	if network == SandboxInternalNetworkName {
		out, err := exec.Command(d.Binary, "network", "inspect", network, "--format", "{{.Internal}}").Output()
		if err == nil && strings.TrimSpace(string(out)) == "false" {
			_ = exec.Command(d.Binary, "network", "rm", network).Run()
			create := exec.Command(d.Binary, "network", "create", "--internal", network)
			if cOut, cErr := create.CombinedOutput(); cErr != nil {
				return fmt.Errorf("recreate sandbox network %q as internal: %s", network, string(cOut))
			}
		}
	}
	return nil
}

// effectiveNetwork resolves the run's network: the request override, the
// runner default, then none — with bridge-needing runs mapped onto the
// dedicated sandbox network.
func (d *Docker) effectiveNetwork(request Request) string {
	network := request.Network
	if network == "" {
		network = d.Network
	}
	if network == "" {
		network = "none"
	}
	if network == "bridge" {
		return SandboxNetworkName
	}
	return network
}

func (d *Docker) arguments(workspace string, request Request, name string, socketArgs []string) []string {
	mode := "rw"
	if request.ReadOnly {
		mode = "ro"
	}
	// /scratch is the exec-capable writable area every command needs: build
	// tools compile and execute artifacts there (go test binaries, npm/pip
	// caches). /tmp stays noexec for plain temp files. HOME and TMPDIR point
	// at /scratch so toolchain defaults work even when the workspace is
	// mounted read-only (reviewer/qa roles).
	// With a ScratchKey the area becomes run-scoped: a host directory under
	// the sandbox root bind-mounted at /scratch, so files survive between
	// commands of one run (a heredoc-then-curl sequence no longer needs to be
	// one command) and the storage sweeper reclaims them by age afterwards.
	// Bind mounts are exec-allowed by default; the size cap of the tmpfs is
	// replaced by the storage sweeper + disk quotas of the sandbox root.
	scratchSize := firstNonEmpty(request.ScratchSize, d.ScratchSize, "512m")
	var scratchArgs []string
	if request.ScratchKey != "" {
		scratchArgs = []string{"--volume", d.runScratchDir(request.ScratchKey) + ":/scratch:rw"}
	} else {
		scratchArgs = []string{"--tmpfs", "/scratch:rw,exec,nosuid,size=" + scratchSize}
	}
	// Toolchains that ignore TMPDIR (some linkers, cgo, packaging steps)
	// still land their temporaries in /tmp; 64m was too tight for real
	// builds, so the cap is configurable and defaults to a size that
	// survives a link step.
	tmpSize := firstNonEmpty(request.TmpSize, d.TmpSize, "256m")
	network := d.effectiveNetwork(request)
	memory := firstNonEmpty(request.Memory, d.Memory)
	cpus := firstNonEmpty(request.CPUs, d.CPUs)
	pids := d.PIDs
	if request.PIDs > 0 {
		pids = request.PIDs
	}
	args := []string{"run", "--init", "--pull=never", "--name", name, "--network=" + network, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit", strconv.Itoa(pids), "--memory", memory, "--memory-swap", memory, "--cpus", cpus, "--ulimit", "nofile=1024:1024", "--user", fmt.Sprintf("%d:%d", d.UID, d.GID), "--workdir", "/workspace", "--tmpfs", "/tmp:rw,noexec,nosuid,size=" + tmpSize}
	args = append(args, scratchArgs...)
	args = append(args, "--env", "HOME=/scratch", "--env", "TMPDIR=/scratch", "--volume", workspace + ":/workspace:" + mode)
	// Socket passthrough flags land with the other volume/env flags — before
	// the image, where every docker argument must stay.
	args = append(args, socketArgs...)
	// Per-project persistent cache: a host directory under the sandbox root,
	// outside any git workspace, so `git clean` and workspace resets cannot
	// destroy toolchain caches. Toolchains see it at /cache; it stays writable
	// even for read-only workspaces because it never carries project files.
	// A profile's CacheEnv replaces the legacy Go-only pointers with its own
	// toolchain caches; GOTMPDIR is a /scratch concern, not a cache one, so it
	// stays for everyone.
	if cache := d.projectCacheDir(workspace, request.CacheEnv); cache != "" {
		args = append(args, "--volume", cache+":/cache:rw", "--env", "GOTMPDIR=/scratch")
		cacheEnv := request.CacheEnv
		if len(cacheEnv) == 0 {
			cacheEnv = []string{"GOCACHE=/cache/go-build", "GOMODCACHE=/cache/go-mod"}
		}
		for _, entry := range cacheEnv {
			args = append(args, "--env", entry)
		}
	}
	for _, entry := range request.Env {
		args = append(args, "--env", entry)
	}
	// Labels sorted by key: identical requests must produce identical argv,
	// matching the deterministic-argv discipline of the rest of the runner.
	for _, key := range slices.Sorted(maps.Keys(request.Labels)) {
		args = append(args, "--label", key+"="+request.Labels[key])
	}
	image := firstNonEmpty(request.Image, d.Image)
	args = append(args, image)
	return append(args, request.Command...)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// RunScratchDir is the exported form of runScratchDir: the file tools map
// /scratch/… paths onto the same host tree run_command mounts at /scratch.
func (d *Docker) RunScratchDir(key string) string { return d.runScratchDir(key) }

// runScratchDir is the host directory behind a run-scoped /scratch: under the
// sandbox root, keyed by "<project>/<run>" so different runs never share
// files and the storage sweeper can reclaim whole trees by age.
func (d *Docker) runScratchDir(key string) string {
	project, run := "_", key
	if i := strings.IndexByte(key, '/'); i >= 0 {
		project, run = key[:i], key[i+1:]
	}
	return filepath.Join(d.Root, "scratch", SanitizeScratchToken(project), SanitizeScratchToken(run))
}

// SanitizeScratchToken keeps filename-safe characters so a hostile project or
// run id can never traverse out of the scratch tree.
func SanitizeScratchToken(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// projectCacheDir returns the host directory mounted at /cache for the
// workspace's project, creating its toolchain subdirectories when needed.
// Empty means "no cache mount": the runner has no root (unit-test harness) or
// the workspace lives outside it (defensive; Execute resolves workspaces
// first, so this never triggers in production). Subdirectories come from the
// profile's cache env — the legacy Go pair without a profile, the profile's
// own toolchains with one.
func (d *Docker) projectCacheDir(workspace string, cacheEnv []string) string {
	if d.Root == "" || workspace == "" {
		return ""
	}
	rel, err := filepath.Rel(d.Root, workspace)
	if err != nil || rel == ".." || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	// The workspace base name is the project id; per-project caches never mix.
	cache := filepath.Join(d.Root, "cache", filepath.Base(workspace))
	subs := []string{"go-build", "go-mod"}
	if len(cacheEnv) > 0 {
		subs = cacheSubdirs(cacheEnv)
	}
	for _, sub := range subs {
		if err := os.MkdirAll(filepath.Join(cache, sub), 0o777); err != nil {
			return ""
		}
	}
	return cache
}

// cacheSubdirs extracts the /cache subdirectories referenced by cache env
// entries so the mount pre-creates them: "NPM_CONFIG_CACHE=/cache/npm"
// yields "npm".
func cacheSubdirs(cacheEnv []string) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, entry := range cacheEnv {
		idx := strings.IndexByte(entry, '=')
		if idx < 0 {
			continue
		}
		value := strings.TrimSuffix(strings.TrimPrefix(entry[idx+1:], "/cache/"), "/")
		if value == "" || strings.Contains(value, "/") || seen[value] {
			continue
		}
		seen[value] = true
		dirs = append(dirs, value)
	}
	return dirs
}

func containerName() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create sandbox container identity: %w", err)
	}
	return "temporality-sandbox-" + hex.EncodeToString(random[:]), nil
}

func (d *Docker) removeContainer(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, d.Binary, "rm", "--force", name).CombinedOutput()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && strings.Contains(strings.ToLower(string(output)), "no such container") {
		return nil
	}
	return fmt.Errorf("remove sandbox container: %w: %s", err, strings.TrimSpace(string(output)))
}

// validateEnv keeps injected variables within docker --env expectations: a
// non-empty KEY of [A-Za-z_][A-Za-z0-9_]* followed by = and any value. The
// total size is capped like command arguments so a huge secret cannot blow up
// argv limits.
func validateEnv(entries []string) error {
	total := 0
	for _, entry := range entries {
		equals := strings.IndexByte(entry, '=')
		if equals <= 0 {
			return fmt.Errorf("env entry %q must be KEY=VALUE", entry)
		}
		key := entry[:equals]
		for i := 0; i < len(key); i++ {
			c := key[i]
			alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
			if i == 0 && !alpha || i > 0 && !(alpha || c >= '0' && c <= '9') {
				return fmt.Errorf("env key %q has invalid characters", key)
			}
		}
		total += len(entry)
	}
	if total > maxTotalArgs {
		return errors.New("env exceeds total size limit")
	}
	return nil
}

func validateCommand(args []string) error {
	if len(args) == 0 || len(args) > maxCommandArgs {
		return fmt.Errorf("command must contain 1 to %d arguments", maxCommandArgs)
	}
	if strings.TrimSpace(args[0]) == "" {
		return errors.New("command executable is required")
	}
	total := 0
	for _, arg := range args {
		if len(arg) > maxArgBytes {
			return errors.New("command argument exceeds size limit")
		}
		total += len(arg)
	}
	if total > maxTotalArgs {
		return errors.New("command exceeds total size limit")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	truncated bool
	// stream names this buffer's data for onChunk ("stdout" | "stderr").
	// onChunk, when set, sees every byte written — before the 64 KiB result
	// truncation below, because the console tail keeps its own, larger budget
	// (v2-consolidated.md §6: 256 KiB / 2000 lines per stream). The final
	// Result.Output stays bounded as before.
	stream  string
	onChunk func(stream string, chunk []byte)
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.onChunk != nil && len(p) > 0 {
		b.onChunk(b.stream, p)
	}
	n := len(p)
	remaining := maxOutputBytes - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.Buffer.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.Buffer.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return n, nil
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
