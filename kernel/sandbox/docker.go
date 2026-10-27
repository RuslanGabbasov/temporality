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
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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

	// networkMu guards networkChecked: the networks whose presence (and, for
	// the internal variant, --internal flag) ensureNetwork has already
	// verified in this process. Keyed by resolved network name; a failed run
	// forgets its entry so the next command re-verifies — a compose rebuild
	// can delete the network under a live kernel.
	networkMu      sync.Mutex
	networkChecked map[string]bool

	// runs is the registry of live keep-alive containers keyed by the run's
	// ScratchKey ("<project>/<run>"): the first command of a run pays one
	// `docker run -d`, every further command execs into the same container
	// instead of paying the full run+rm cycle again. runMu guards the map and
	// every entry's counters; createMu serializes container creation and
	// teardown (docker run -d costs hundreds of ms, and two concurrent
	// creators for one key must not race two containers into one slot).
	runMu    sync.Mutex
	runs     map[string]*runContainer
	createMu sync.Mutex
	// idleTTL is how long an unused run container survives before the reaper
	// stops it (KERNEL_SANDBOX_CONTAINER_IDLE). Zero disables the reaper —
	// the default for directly-constructed runners, so tests stay
	// deterministic; newDocker fills it from the environment.
	idleTTL    time.Duration
	reaperOnce sync.Once
}

// runContainer is one live keep-alive container of the run registry.
type runContainer struct {
	// key is the run identity the container serves (the ScratchKey); name is
	// the random docker name it was created with.
	key         string
	name        string
	fingerprint string
	// lastUsed dates the START of the most recent command: the reaper's idle
	// clock measures quiet time between commands, not their length.
	lastUsed time.Time
	// busy counts in-flight commands. evict marks an entry whose container
	// must die as soon as the last in-flight command releases it — an exec
	// is never killed from underneath by our own cleanup.
	busy  int
	evict bool
}

// keepAliveCommand holds a run container open between the commands of one
// run. Every supported base ships sleep and accepts "infinity" (the alpine
// profiles run busybox, the playwright profile GNU coreutils), so the flag
// tail needs no shell and no entrypoint override; with --init the sleeper
// sits under tini, so `docker rm --force` reaches it cleanly.
var keepAliveCommand = []string{"sleep", "infinity"}

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
	return &Docker{Root: resolved, Image: image, Binary: env("KERNEL_DOCKER_BINARY", "docker"), Memory: env("KERNEL_SANDBOX_MEMORY", "1g"), CPUs: clampCPUs(env("KERNEL_SANDBOX_CPUS", "2"), runtime.NumCPU()), PIDs: pidsFromEnv("KERNEL_SANDBOX_PIDS", 512), UID: os.Getuid(), GID: os.Getgid(), ScratchSize: env("KERNEL_SANDBOX_SCRATCH_SIZE", "512m"), TmpSize: env("KERNEL_SANDBOX_TMP_SIZE", "256m"), idleTTL: containerIdleTTL()}, nil
}

// commandTimeout resolves the effective per-command budget: the request's
// value, the 300s default when the model passed none, capped at the ceiling.
func commandTimeout(requested int) int {
	timeout := requested
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout() {
		timeout = maxTimeout()
	}
	return timeout
}

// defaultContainerIdle keeps an unused run container alive for a quarter
// hour: runs idle between turns (model calls, human input) for minutes, so
// anything shorter would tear down containers the next turn still needs.
const defaultContainerIdle = 15 * time.Minute

// containerIdleTTL parses KERNEL_SANDBOX_CONTAINER_IDLE: plain minutes
// ("15") or a duration ("90s", "2m"). Zero disables the idle reaper
// entirely; junk falls back to the default rather than silently disabling
// the cleanup an operator asked for.
func containerIdleTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_CONTAINER_IDLE"))
	if raw == "" {
		return defaultContainerIdle
	}
	if minutes, err := strconv.Atoi(raw); err == nil {
		if minutes < 0 {
			return defaultContainerIdle
		}
		return time.Duration(minutes) * time.Minute
	}
	if duration, err := time.ParseDuration(raw); err == nil && duration >= 0 {
		return duration
	}
	return defaultContainerIdle
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
	network := d.effectiveNetwork(request)
	timeout := commandTimeout(request.TimeoutSeconds)
	if request.ScratchKey == "" {
		// No stable run identity to reuse a container against (direct runner
		// callers, unit-test harnesses): the one-shot lifecycle stays.
		return d.executeOneShot(parent, request, workspace, socketArgs, network, timeout)
	}
	return d.executeReused(parent, request, workspace, socketArgs, timeout)
}

// executeOneShot is the classic per-command lifecycle: one `docker run` with
// the full flag set, the command as the container's payload, `docker rm
// --force` right after. 0.4-1.3s of daemon overhead per command; kept for
// requests without a ScratchKey.
func (d *Docker) executeOneShot(parent context.Context, request Request, workspace string, socketArgs []string, network string, timeout int) (result Result, runErr error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	name, err := containerName()
	if err != nil {
		return Result{}, err
	}
	args := d.arguments(workspace, request, name, socketArgs)
	if err := d.ensureNetwork(network); err != nil {
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
	if err != nil {
		// The failed run may be the network's fault (deleted under a live
		// kernel) — drop the memoized state so the next command re-verifies.
		d.forgetNetwork(network)
	}
	result = Result{Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	result.Output = commandOutput(&stdout, &stderr)
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

// createFailure carries a refused container creation as the failed-command
// result the one-shot path produced for the same daemon error (a missing
// image surfaced as exit 125 plus the daemon text, not an Execute error).
type createFailure struct{ result Result }

func (e *createFailure) Error() string { return strings.TrimSpace(e.result.Output) }

// executeReused runs one command of a run against the run's keep-alive
// container: the first command of the run pays one `docker run -d`, every
// further command is a `docker exec` into it — the per-command overhead
// drops from a full container create/remove cycle to one exec call. Timeouts
// and confirmed daemon deaths evict the container; the next command
// recreates it.
func (d *Docker) executeReused(parent context.Context, request Request, workspace string, socketArgs []string, timeout int) (Result, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	d.startReaper()
	for attempt := 0; ; attempt++ {
		rc, err := d.acquireRunContainer(ctx, request, workspace, socketArgs, timeout)
		if err != nil {
			var cf *createFailure
			if errors.As(err, &cf) {
				return cf.result, nil
			}
			return Result{}, err
		}
		result, execErr := d.execCommand(ctx, rc, request)
		d.releaseRunContainer(rc)
		if execErr == nil {
			return result, nil
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// A timed-out exec has no kill switch of its own — docker exec
			// cannot address the process it started. Stop the whole container
			// (rm --force = stop + remove, nothing lingers) and evict it: the
			// next command of the run starts clean.
			d.evictRunContainer(rc)
			return result, fmt.Errorf("sandbox command exceeded %d second timeout", timeout)
		}
		var exitErr *exec.ExitError
		if !errors.As(execErr, &exitErr) {
			return result, fmt.Errorf("exec sandbox command: %w", execErr)
		}
		// The docker CLI ran and exited non-zero. Daemon refusals print their
		// phrasing into our stderr capture; a dead container gets exactly one
		// recreate-and-retry. The confirm step keeps a command that merely
		// printed the phrase (grep -r "is not running" over logs) from being
		// rerun against a healthy container.
		if attempt == 0 && deadContainerOutput(result.Output) && d.containerDead(rc) {
			d.evictRunContainer(rc)
			continue
		}
		// Plain command failure — or the retry failed again: non-zero exit is
		// a result, not an error, the same contract the one-shot path has.
		return result, nil
	}
}

// execCommand runs one command in the run container via `docker exec`. The
// raw CLI error is returned for the caller to classify: a non-zero exit is a
// command result, anything else is infrastructure.
func (d *Docker) execCommand(ctx context.Context, rc *runContainer, request Request) (Result, error) {
	started := time.Now()
	var stdout, stderr limitedBuffer
	stdout.onChunk, stderr.onChunk = request.OnOutput, request.OnOutput
	stdout.stream, stderr.stream = "stdout", "stderr"
	cmd := exec.CommandContext(ctx, d.Binary, d.execArgs(rc, request)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := Result{Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	result.Output = commandOutput(&stdout, &stderr)
	return result, err
}

// deadContainerOutput recognizes the daemon refusal texts `docker exec`
// prints when the target container is gone, stopped or paused. It is a hint,
// never the verdict — the containerDead inspect decides.
func deadContainerOutput(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "is not running") ||
		strings.Contains(lower, "no such container") ||
		strings.Contains(lower, "is paused") ||
		strings.Contains(lower, "cannot exec in a stopped container")
}

// containerDead asks the daemon whether the container still exists and runs.
// Only dead-container-looking exec failures pay this one inspect, so the
// recovery path never reruns a command whose output merely contained the
// phrase.
func (d *Docker) containerDead(rc *runContainer) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, d.Binary, "inspect", "-f", "{{.State.Running}}", rc.name).CombinedOutput()
	if err != nil {
		// No such container — or a daemon that cannot answer. Either way the
		// recreate below is the recovery path.
		return true
	}
	return strings.TrimSpace(string(out)) != "true"
}

// acquireRunContainer returns the run's live container with one busy mark
// taken. The happy path is a registry hit under runMu; only a miss (first
// command of the run, or the container was evicted) pays the serialized
// creation. A live entry with a different fingerprint — the run switched
// profile or read-only mode mid-way — is retired, never silently reused for
// execs it was not created for.
func (d *Docker) acquireRunContainer(ctx context.Context, request Request, workspace string, socketArgs []string, timeout int) (*runContainer, error) {
	fingerprint := d.containerFingerprint(workspace, request)
	if rc := d.lookupRun(request.ScratchKey, fingerprint); rc != nil {
		return rc, nil
	}
	d.createMu.Lock()
	defer d.createMu.Unlock()
	if rc := d.lookupRun(request.ScratchKey, fingerprint); rc != nil {
		return rc, nil
	}
	d.TeardownRun(request.ScratchKey)
	return d.createRunContainer(ctx, request, workspace, socketArgs, timeout)
}

// lookupRun returns the run's registered container when its fingerprint
// matches, marking it busy and dating the start of this command.
func (d *Docker) lookupRun(key, fingerprint string) *runContainer {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	rc := d.runs[key]
	if rc == nil || rc.fingerprint != fingerprint {
		return nil
	}
	rc.busy++
	rc.lastUsed = time.Now()
	return rc
}

// createRunContainer materializes the run's keep-alive container with the
// full isolation contour of a one-shot run and registers it busy (the
// calling command owns one count).
func (d *Docker) createRunContainer(ctx context.Context, request Request, workspace string, socketArgs []string, timeout int) (*runContainer, error) {
	// Run-scoped /scratch: the host directory must exist before the bind
	// mount (docker would create it root-owned otherwise, and the sandbox
	// user could not write into it).
	if err := os.MkdirAll(d.runScratchDir(request.ScratchKey), 0o777); err != nil {
		return nil, fmt.Errorf("run scratch directory: %w", err)
	}
	network := d.effectiveNetwork(request)
	if err := d.ensureNetwork(network); err != nil {
		return nil, err
	}
	name, err := containerName()
	if err != nil {
		return nil, err
	}
	var output limitedBuffer
	cmd := exec.CommandContext(ctx, d.Binary, d.keepAliveArgs(workspace, request, name, socketArgs)...)
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		d.forgetNetwork(network)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("sandbox command exceeded %d second timeout", timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &createFailure{result: Result{ExitCode: cmd.ProcessState.ExitCode(), Duration: 0, Output: commandOutput(&output, &limitedBuffer{})}}
		}
		return nil, fmt.Errorf("create sandbox container: %w: %s", err, strings.TrimSpace(output.String()))
	}
	rc := &runContainer{key: request.ScratchKey, name: name, fingerprint: d.containerFingerprint(workspace, request), lastUsed: time.Now(), busy: 1}
	d.storeRun(rc)
	return rc, nil
}

func (d *Docker) storeRun(rc *runContainer) {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	if d.runs == nil {
		d.runs = make(map[string]*runContainer)
	}
	d.runs[rc.key] = rc
}

// releaseRunContainer hands the container back: the last release of an
// evicted entry stops it.
func (d *Docker) releaseRunContainer(rc *runContainer) {
	d.runMu.Lock()
	rc.busy--
	evict := rc.evict && rc.busy <= 0
	d.runMu.Unlock()
	if evict {
		_ = d.removeContainer(rc.name)
	}
}

// evictRunContainer drops the entry from the registry and stops its
// container — after a timeout (a runaway exec cannot be killed alone) or a
// confirmed daemon death. A container with in-flight commands cannot die
// yet; the evict flag makes its last releasing command do it.
func (d *Docker) evictRunContainer(rc *runContainer) {
	d.runMu.Lock()
	if cur := d.runs[rc.key]; cur == rc {
		delete(d.runs, rc.key)
	}
	if rc.busy > 0 {
		rc.evict = true
		d.runMu.Unlock()
		return
	}
	d.runMu.Unlock()
	_ = d.removeContainer(rc.name)
}

// TeardownRun stops and forgets the run's keep-alive container. The idle
// reaper is the by-age safety net; this is the deterministic counterpart,
// called when the run's scratch tree is swept — scratch and container share
// the "<project>/<run>" key, so the sweep is the one moment the run is known
// to be over. A container with an in-flight command is flagged instead of
// force-removed; its releasing command stops it.
func (d *Docker) TeardownRun(key string) {
	d.runMu.Lock()
	rc := d.runs[key]
	delete(d.runs, key)
	if rc != nil && rc.busy > 0 {
		rc.evict = true
		rc = nil
	}
	d.runMu.Unlock()
	if rc != nil {
		_ = d.removeContainer(rc.name)
	}
}

// startReaper launches the lazy reaper once per runner: containers unused
// for longer than idleTTL are stopped, so a finished (or crashed) run's
// keep-alive container cannot outlive its run by hours. Disabled with
// idleTTL 0 — directly-constructed runners (tests) never start one.
func (d *Docker) startReaper() {
	if d.idleTTL <= 0 {
		return
	}
	d.reaperOnce.Do(func() {
		period := d.idleTTL / 2
		if period > time.Minute {
			period = time.Minute
		}
		go func() {
			ticker := time.NewTicker(period)
			for range ticker.C {
				d.reapIdle()
			}
		}()
	})
}

func (d *Docker) reapIdle() {
	for _, rc := range d.idleContainers(time.Now()) {
		d.evictRunContainer(rc)
	}
}

// idleContainers picks the registry entries whose last command started
// longer than idleTTL ago, removing them from the registry. Busy containers
// always stay: lastUsed only dates the START of the current command, and a
// long build must never be rm -f'd mid-flight.
func (d *Docker) idleContainers(now time.Time) []*runContainer {
	d.runMu.Lock()
	defer d.runMu.Unlock()
	var idle []*runContainer
	for _, rc := range d.runs {
		if rc.busy == 0 && now.Sub(rc.lastUsed) > d.idleTTL {
			delete(d.runs, rc.key)
			idle = append(idle, rc)
		}
	}
	return idle
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
//
// The docker CLI inspection is memoized per network for the process lifetime:
// the first network-needing command pays for it, the rest skip straight to
// `docker run` (every poll-cycle command used to run one — two for the
// egress network — before even starting). The cache forgets an entry when a
// run fails (forgetNetwork), so a network deleted under a live kernel is
// re-created on the next command instead of failing forever.
func (d *Docker) ensureNetwork(network string) error {
	if network != SandboxNetworkName && network != SandboxInternalNetworkName {
		return nil
	}
	if d.verifiedNetwork(network) {
		return nil
	}
	if err := d.inspectOrCreateNetwork(network); err != nil {
		return err
	}
	d.rememberNetwork(network)
	return nil
}

// inspectOrCreateNetwork is ensureNetwork's uncached body: one `docker
// network inspect` presence probe (plus the Internal-flag verification for
// the egress variant — once per process thanks to the memoization above).
func (d *Docker) inspectOrCreateNetwork(network string) error {
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

// verifiedNetwork reports whether ensureNetwork already materialized this
// network in the current process.
func (d *Docker) verifiedNetwork(network string) bool {
	d.networkMu.Lock()
	defer d.networkMu.Unlock()
	return d.networkChecked[network]
}

func (d *Docker) rememberNetwork(network string) {
	d.networkMu.Lock()
	defer d.networkMu.Unlock()
	if d.networkChecked == nil {
		d.networkChecked = make(map[string]bool)
	}
	d.networkChecked[network] = true
}

// forgetNetwork drops the memoized state for one network: the run that just
// failed may have failed because of it (a stack rebuild deletes the network
// under a live kernel), so the next command must re-inspect instead of
// failing forever. Over-invalidating — any run error clears the entry — is
// deliberate and cheap: one extra `docker network inspect` per failed
// command.
func (d *Docker) forgetNetwork(network string) {
	d.networkMu.Lock()
	defer d.networkMu.Unlock()
	delete(d.networkChecked, network)
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

// containerFingerprint captures everything baked into a container at
// `docker run -d` time. Two commands of one run share one container only
// when their shapes match: a run that switches profile (a writable build
// step, then a read-only review step), image or network gets a fresh
// container. Per-command inputs deliberately stay out — env/secrets ride on
// exec, command and timeout are exec arguments, labels are observability
// only (the first command's labels stay for the container's lifetime).
func (d *Docker) containerFingerprint(workspace string, request Request) string {
	return fmt.Sprintf("%t|%q|%q|%q|%q|%d|%q|%q|%q|%t|%q|%d:%d",
		request.ReadOnly, workspace, request.Image, request.Memory, request.CPUs, request.PIDs,
		request.ScratchSize, request.TmpSize, fmt.Sprint(request.CacheEnv), request.DockerSocket,
		d.effectiveNetwork(request), d.UID, d.GID)
}

// runFlags renders every docker run argument before the image reference: the
// isolation contour (read-only root, cap-drop, resource limits), the mounts
// and the baked environment. detach marks the keep-alive creation (`docker
// run -d`). Everything per-command by nature — injected env/secrets, the
// command itself — never lands here: it rides on `docker exec` over the
// reused container instead.
func (d *Docker) runFlags(workspace string, request Request, name string, socketArgs []string, detach bool) []string {
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
	args := []string{"run"}
	if detach {
		args = append(args, "-d")
	}
	args = append(args, "--init", "--pull=never", "--name", name, "--network="+network, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit", strconv.Itoa(pids), "--memory", memory, "--memory-swap", memory, "--cpus", cpus, "--ulimit", "nofile=1024:1024", "--user", fmt.Sprintf("%d:%d", d.UID, d.GID), "--workdir", "/workspace", "--tmpfs", "/tmp:rw,noexec,nosuid,size="+tmpSize)
	args = append(args, scratchArgs...)
	args = append(args, "--env", "HOME=/scratch", "--env", "TMPDIR=/scratch", "--volume", workspace+":/workspace:"+mode)
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
	// Labels sorted by key: identical requests must produce identical argv,
	// matching the deterministic-argv discipline of the rest of the runner.
	for _, key := range slices.Sorted(maps.Keys(request.Labels)) {
		args = append(args, "--label", key+"="+request.Labels[key])
	}
	return args
}

// arguments renders the one-shot `docker run`: the full flag set, the image,
// and the command as the container payload — including the per-command env.
func (d *Docker) arguments(workspace string, request Request, name string, socketArgs []string) []string {
	args := d.runFlags(workspace, request, name, socketArgs, false)
	for _, entry := range request.Env {
		args = append(args, "--env", entry)
	}
	image := firstNonEmpty(request.Image, d.Image)
	args = append(args, image)
	return append(args, request.Command...)
}

// keepAliveArgs renders the `docker run -d` that creates a run container:
// the same isolation contour and mounts as a one-shot run, but no command —
// the keep-alive PID1 holds the container open for the whole run's execs.
// Injected env/secrets stay out on purpose: they are per-command and go
// through exec (execArgs), so one command's secret never bakes into the
// environment of every later command of the run.
func (d *Docker) keepAliveArgs(workspace string, request Request, name string, socketArgs []string) []string {
	args := d.runFlags(workspace, request, name, socketArgs, true)
	args = append(args, firstNonEmpty(request.Image, d.Image))
	return append(args, keepAliveCommand...)
}

// execArgs renders `docker exec` over a live run container. Docker exec
// supports no resource, network, mount or security flags — those are baked
// into the container at creation (runFlags) and cover every exec
// automatically. What moves here from the old per-command run flags is the
// per-command part: injected env/secrets, plus the workdir and user that
// used to be run flags and must therefore be restated on every exec.
func (d *Docker) execArgs(rc *runContainer, request Request) []string {
	args := []string{"exec"}
	for _, entry := range request.Env {
		args = append(args, "--env", entry)
	}
	args = append(args, "--workdir", "/workspace", "--user", fmt.Sprintf("%d:%d", d.UID, d.GID), rc.name)
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

// ReadFrom shadows the bytes.Buffer.ReadFrom promoted by embedding:
// os/exec copies process output with io.Copy, which prefers io.ReaderFrom.
// The promoted method would fill the buffer directly, bypassing Write —
// and with it both onChunk streaming and the maxOutputBytes truncation.
func (b *limitedBuffer) ReadFrom(r io.Reader) (int64, error) {
	// struct{io.Writer} strips ReaderFrom, forcing io.Copy's chunked Write loop.
	return io.Copy(struct{ io.Writer }{b}, r)
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

// commandOutput merges the two stream buffers into Result.Output the way it
// has always been carried: stdout, then stderr, then the truncation marker.
func commandOutput(stdout, stderr *limitedBuffer) string {
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
	return output
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
