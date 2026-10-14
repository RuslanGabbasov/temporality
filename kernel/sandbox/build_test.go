package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestSocketErrorPermissionDenied(t *testing.T) {
	raw := "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Post \"http://%2Fvar%2Frun%2Fdocker.sock/v1.47/version\": dial unix /var/run/docker.sock: connect: permission denied"
	err := socketError(raw, errors.New("exit status 1"), 10001)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"uid=10001", "TEMPORALITY_DOCKER_GID", "docker compose up -d --build agent-kernel"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	if strings.Count(msg, "\n") != 1 {
		t.Errorf("expected a single hint line, got: %s", msg)
	}
}

func TestSocketErrorDaemonUnreachable(t *testing.T) {
	err := socketError("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", errors.New("exit status 1"), 10001)
	if err == nil || !strings.Contains(err.Error(), "not reachable") || !strings.Contains(err.Error(), "docker.sock") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSocketErrorPassthrough(t *testing.T) {
	err := socketError("some other failure", errors.New("boom"), 10001)
	if err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "TEMPORALITY_DOCKER_GID") {
		t.Fatalf("unexpected error: %v", err)
	}
}
