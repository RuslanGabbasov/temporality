package sandbox

import (
	"context"
	"errors"
	"sync"
)

// ErrNotConfigured is returned by a Swappable runner with no backing
// implementation: tool registration checks Configured before advertising
// run_command, so reaching this error means the configuration changed
// between two steps of one run.
var ErrNotConfigured = errors.New("sandbox is not configured")

// Swappable is a Runner whose backing implementation can be replaced at
// runtime. The first-run onboarding builds and pins the sandbox image after
// the kernel started, so the runner installed by NewFromEnv (or none at all)
// must be exchangeable without a restart. Reads take the fast RLock path; the
// swap happens once per build.
type Swappable struct {
	mu      sync.RWMutex
	current Runner
}

// NewSwappable wraps an initial runner; initial may be nil (not configured).
func NewSwappable(initial Runner) *Swappable {
	return &Swappable{current: initial}
}

// Swap installs r as the backing runner and returns the previous one.
func (s *Swappable) Swap(r Runner) Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.current
	s.current = r
	return old
}

// Current returns the backing runner (nil when not configured).
func (s *Swappable) Current() Runner {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Configured reports whether a backing runner is installed.
func (s *Swappable) Configured() bool {
	return s.Current() != nil
}

// Image returns the digest-pinned image reference of the backing Docker
// runner, or "" when not configured.
func (s *Swappable) Image() string {
	if d, ok := s.Current().(*Docker); ok && d != nil {
		return d.Image
	}
	return ""
}

func (s *Swappable) SandboxRoot() string {
	if current := s.Current(); current != nil {
		return current.SandboxRoot()
	}
	return ""
}

func (s *Swappable) ResolveWorkspace(path string) (string, error) {
	current := s.Current()
	if current == nil {
		return "", ErrNotConfigured
	}
	return current.ResolveWorkspace(path)
}

// RunScratchDir maps a "<project>/<run>" scratch key onto the host directory
// the backing runner mounts at /scratch; empty when not configured.
func (s *Swappable) RunScratchDir(key string) string {
	current := s.Current()
	if current == nil {
		return ""
	}
	return current.RunScratchDir(key)
}

func (s *Swappable) Execute(ctx context.Context, request Request) (Result, error) {
	current := s.Current()
	if current == nil {
		return Result{}, ErrNotConfigured
	}
	return current.Execute(ctx, request)
}
