package workspace

import (
	"strings"
	"testing"
)

// Runtime-only edits (limits, cache env, socket, name/description) must not
// un-build a profile: «нужна сборка» — только после изменения рецепта.
// The edit form sends no image for recipe profiles (it is build output, not
// a form field), so the update must preserve the stored pin itself.
func TestPrepareSandboxProfileUpdateKeepsImageOnRuntimeEdits(t *testing.T) {
	digest := "registry.local/toolbox@sha256:" + strings.Repeat("a", 64)

	// Recipe profile: resource-only PUT (the form's shape: image empty).
	existing := SandboxProfile{ID: "p1", Name: "Custom", Base: "node", Packages: []string{"git"}, Image: digest}
	incoming := SandboxProfile{ID: "p1", Name: "Custom", Base: "node", Packages: []string{"git"}, Memory: "4g", CPUs: "2"}
	if got := prepareSandboxProfileUpdate(existing, incoming); got.Image != digest {
		t.Errorf("runtime edit un-built the recipe profile: image = %q", got.Image)
	}

	// Recipe change still invalidates the pin.
	changed := SandboxProfile{ID: "p1", Name: "Custom", Base: "node", Packages: []string{"git", "curl"}}
	if got := prepareSandboxProfileUpdate(existing, changed); got.Image != "" {
		t.Errorf("recipe change must clear the pin, got %q", got.Image)
	}

	// Dockerfile-override profile: resource edit keeps the pin, edit does not.
	existing = SandboxProfile{ID: "p2", DockerfileOverride: "FROM alpine", Image: digest}
	incoming = SandboxProfile{ID: "p2", DockerfileOverride: "FROM alpine", ScratchSize: "2g"}
	if got := prepareSandboxProfileUpdate(existing, incoming); got.Image != digest {
		t.Errorf("runtime edit un-built the override profile: image = %q", got.Image)
	}
	incoming = SandboxProfile{ID: "p2", DockerfileOverride: "FROM alpine:3.20"}
	if got := prepareSandboxProfileUpdate(existing, incoming); got.Image != "" {
		t.Errorf("override change must clear the pin, got %q", got.Image)
	}

	// External-image profile: a blank image in a resource PUT keeps the pin…
	existing = SandboxProfile{ID: "p3", Image: digest}
	incoming = SandboxProfile{ID: "p3", Memory: "2g"}
	if got := prepareSandboxProfileUpdate(existing, incoming); got.Image != digest {
		t.Errorf("runtime edit un-built the external profile: image = %q", got.Image)
	}
	// …while an explicit new digest is an intentional re-point.
	other := "registry.local/toolbox@sha256:" + strings.Repeat("b", 64)
	incoming = SandboxProfile{ID: "p3", Image: other}
	if got := prepareSandboxProfileUpdate(existing, incoming); got.Image != other {
		t.Errorf("explicit image swap must land, got %q", got.Image)
	}
}
