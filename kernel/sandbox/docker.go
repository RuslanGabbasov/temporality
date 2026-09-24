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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maxCommandArgs = 128
	maxArgBytes    = 8192
	maxOutputBytes = 64 << 10
	defaultTimeout = 60
	maxTimeout     = 600
)

type Request struct {
	Workspace      string
	Command        []string
	TimeoutSeconds int
	ReadOnly       bool
}

type Result struct {
	Output   string        `json:"output"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
}

type Runner interface {
	Execute(context.Context, Request) (Result, error)
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
}

func NewFromEnv() (*Docker, error) {
	image := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_IMAGE"))
	root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
	if image == "" && root == "" {
		return nil, nil
	}
	if image == "" || root == "" {
		return nil, errors.New("KERNEL_SANDBOX_IMAGE and KERNEL_SANDBOX_ROOT must be configured together")
	}
	if err := validateImage(image); err != nil {
		return nil, err
	}
	if err := validateByteSize(strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_SCRATCH_SIZE"))); err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve KERNEL_SANDBOX_ROOT: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve KERNEL_SANDBOX_ROOT: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return nil, errors.New("KERNEL_SANDBOX_ROOT must be an existing directory")
	}
	return &Docker{Root: resolved, Image: image, Binary: env("KERNEL_DOCKER_BINARY", "docker"), Memory: env("KERNEL_SANDBOX_MEMORY", "1g"), CPUs: env("KERNEL_SANDBOX_CPUS", "2"), PIDs: 128, UID: os.Getuid(), GID: os.Getgid(), ScratchSize: env("KERNEL_SANDBOX_SCRATCH_SIZE", "512m")}, nil
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
	workspace, err := d.ResolveWorkspace(request.Workspace)
	if err != nil {
		return Result{}, err
	}
	timeout := request.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	name, err := containerName()
	if err != nil {
		return Result{}, err
	}
	args := d.arguments(workspace, request, name)
	defer func() {
		if cleanupErr := d.removeContainer(name); cleanupErr != nil {
			runErr = errors.Join(runErr, cleanupErr)
		}
	}()
	started := time.Now()
	var stdout, stderr limitedBuffer
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

func (d *Docker) arguments(workspace string, request Request, name string) []string {
	mode := "rw"
	if request.ReadOnly {
		mode = "ro"
	}
	// /scratch is the exec-capable writable area every command needs: build
	// tools compile and execute artifacts there (go test binaries, npm/pip
	// caches). /tmp stays noexec for plain temp files. HOME and TMPDIR point
	// at /scratch so toolchain defaults work even when the workspace is
	// mounted read-only (reviewer/qa roles).
	scratchSize := d.ScratchSize
	if scratchSize == "" {
		scratchSize = "512m"
	}
	args := []string{"run", "--init", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit", strconv.Itoa(d.PIDs), "--memory", d.Memory, "--memory-swap", d.Memory, "--cpus", d.CPUs, "--ulimit", "nofile=1024:1024", "--user", fmt.Sprintf("%d:%d", d.UID, d.GID), "--workdir", "/workspace", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--tmpfs", "/scratch:rw,exec,nosuid,size=" + scratchSize, "--env", "HOME=/scratch", "--env", "TMPDIR=/scratch", "--volume", workspace + ":/workspace:" + mode, d.Image}
	return append(args, request.Command...)
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
	if total > 32<<10 {
		return errors.New("command exceeds total size limit")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
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
