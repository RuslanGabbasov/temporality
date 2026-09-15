package world

import (
	"strings"
	"testing"
)

func testWorld() World {
	return World{
		WorldID:      "workspace-test",
		StateVersion: 1,
		Resources: []Resource{
			{ID: "repo", Type: ResourceGitRepository, Path: "/workspace/repo"},
			{ID: "api", Type: ResourceHTTPEndpoint, Endpoint: "https://api.example.com"},
		},
		Capabilities: []string{"filesystem.read", "git.read", "http.read"},
		Limits:       Limits{MaxReadBytes: 1024, MaxEntries: 10, TimeoutSec: 5},
	}
}

func TestWorldValidation(t *testing.T) {
	value := testWorld()
	value.Protocol, value.Version = "frp", "0.3"
	if err := value.Validate(); err != nil {
		t.Fatalf("valid world rejected: %v", err)
	}
}

func TestWorldValidationRejectsUnknownCapability(t *testing.T) {
	value := testWorld()
	value.Protocol, value.Version = "frp", "0.3"
	value.Capabilities = append(value.Capabilities, "teleport.anywhere")
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("unknown capability accepted: %v", err)
	}
}

func TestWorldValidationRejectsPolicyForUngrantedCapability(t *testing.T) {
	value := testWorld()
	value.Protocol, value.Version = "frp", "0.3"
	value.Policies = []Policy{{Effect: PolicyDeny, Capability: "process.execute"}}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("policy for ungranted capability accepted: %v", err)
	}
}

func TestWorldAuthorize(t *testing.T) {
	value := testWorld()
	value.Protocol, value.Version = "frp", "0.3"
	if err := value.Authorize("filesystem.read"); err != nil {
		t.Fatalf("granted capability denied: %v", err)
	}
	if err := value.Authorize("filesystem.write"); err == nil {
		t.Fatal("ungranted capability authorized")
	}
	value.Policies = append(value.Policies, Policy{Effect: PolicyDeny, Capability: "git.read"})
	if err := value.Authorize("git.read"); err == nil {
		t.Fatal("denied capability authorized")
	}
	if err := value.AuthorizeAll([]string{"filesystem.read", "git.read"}); err == nil {
		t.Fatal("authorize-all ignored denial")
	}
}

func TestWorldDefaultsAndLimitBounds(t *testing.T) {
	value := World{WorldID: "w", StateVersion: 1, Protocol: "frp", Version: "0.3"}
	value.ApplyDefaults()
	if err := value.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if value.Limits.MaxReadBytes != DefaultMaxReadBytes || value.Limits.MaxEntries != DefaultMaxEntries {
		t.Fatalf("unexpected defaults: %#v", value.Limits)
	}
	value.Limits.MaxReadBytes = MaxAllowedReadBytes + 1
	if err := value.Validate(); err == nil {
		t.Fatal("limit bound not enforced")
	}
}

func TestResourceValidation(t *testing.T) {
	if err := (Resource{ID: "browser", Type: ResourceBrowser, Endpoint: "http://localhost:9222"}).Validate(); err != nil {
		t.Fatalf("browser resource rejected: %v", err)
	}
	if err := (Resource{ID: "x", Type: ResourceFilesystem}).Validate(); err == nil {
		t.Fatal("filesystem resource without path accepted")
	}
	if err := (Resource{ID: "x", Type: ResourceHTTPEndpoint, Endpoint: "ftp://example.com"}).Validate(); err == nil {
		t.Fatal("non-http endpoint accepted")
	}
	if err := (Resource{ID: "Bad", Type: ResourceFilesystem, Path: "/tmp"}).Validate(); err == nil {
		t.Fatal("invalid resource id accepted")
	}
}

func TestCapabilityRegistry(t *testing.T) {
	for _, capability := range CanonicalCapabilities() {
		category, ok := CapabilityCategory(capability)
		if !ok || !category.Valid() {
			t.Fatalf("capability %q has no valid category", capability)
		}
	}
	if _, ok := CapabilityCategory("filesystem.teleport"); ok {
		t.Fatal("unknown capability resolved")
	}
}
