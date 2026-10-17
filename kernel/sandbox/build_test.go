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

func TestCustomDefinition(t *testing.T) {
	def, err := CustomDefinition("qa-tools", "python", []string{"gcc", "linux-headers"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Tag != "temporality-sandbox-qa-tools:latest" {
		t.Errorf("unexpected tag %q", def.Tag)
	}
	// The composed recipe keeps the base's CLI layer and appends the extra
	// apk line for an alpine base.
	for _, want := range []string{"FROM python:3.12-alpine", "apk add --no-cache git", "apk add --no-cache gcc linux-headers"} {
		if !strings.Contains(def.Dockerfile, want) {
			t.Errorf("dockerfile missing %q:\n%s", want, def.Dockerfile)
		}
	}

	// Debian base (browser-e2e) installs extras with apt.
	def, err = CustomDefinition("db-tests", "browser-e2e", []string{"postgresql-client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(def.Dockerfile, "apt-get install -y --no-install-recommends postgresql-client") {
		t.Errorf("dockerfile missing apt line:\n%s", def.Dockerfile)
	}

	// The embedded minimal base is a valid base too.
	if _, err = CustomDefinition("shell-only", "base", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No packages: the recipe equals the base recipe with a fresh tag.
	plain, err := CustomDefinition("plain", "node", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	baseDef, _ := BaseDefinition("node")
	if plain.Dockerfile != baseDef.Dockerfile {
		t.Errorf("package-free recipe must equal the base recipe")
	}
}

func TestCustomDefinitionRejects(t *testing.T) {
	if _, err := CustomDefinition("Bad_ID", "python", nil); err == nil {
		t.Error("expected error for invalid profile id")
	}
	if _, err := CustomDefinition("ok-id", "invented-base", nil); err == nil {
		t.Error("expected error for unknown base")
	}
	// Shell metacharacters never reach the RUN line.
	for _, bad := range []string{"gcc; rm -rf /", "$(id)", "a b", "x`id`"} {
		if _, err := CustomDefinition("ok-id", "python", []string{bad}); err == nil {
			t.Errorf("expected error for package %q", bad)
		}
	}
	sixtyFive := make([]string, 65)
	for i := range sixtyFive {
		sixtyFive[i] = "pkg"
	}
	if _, err := CustomDefinition("ok-id", "python", sixtyFive); err == nil {
		t.Error("expected error for too many packages")
	}
	if !ValidPackageName("libicu-dev") || !ValidPackageName("python3-dev") || !ValidPackageName("postgresql-client") {
		t.Error("valid package names rejected")
	}
	if ValidPackageName("GCC") || ValidPackageName("") || ValidPackageName("a/b") {
		t.Error("invalid package name accepted")
	}
}
