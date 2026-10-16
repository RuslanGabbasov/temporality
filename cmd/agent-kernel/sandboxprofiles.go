package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Sandbox profile registry routes (docs/sandbox-profiles.md §6): list with
// local availability, custom create/update/delete and builtin image build.
// Building an image changes what model-authored commands may execute, so
// builds are writer-only and audited.

// sandboxProfileStore is the storage surface this router needs;
// *workspace.Store satisfies it and tests provide fakes.
type sandboxProfileStore interface {
	ListSandboxProfiles(ctx context.Context) ([]workspace.SandboxProfile, error)
	GetSandboxProfile(ctx context.Context, id string) (workspace.SandboxProfile, error)
	CreateSandboxProfile(ctx context.Context, p *workspace.SandboxProfile) error
	UpdateSandboxProfile(ctx context.Context, p workspace.SandboxProfile) error
	SetSandboxProfileImage(ctx context.Context, id, image string) error
	DeleteSandboxProfile(ctx context.Context, id string) error
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// sandboxProfileOutbox is the durable-outbox surface for registry events.
type sandboxProfileOutbox interface {
	Enqueue(context.Context, observation.Event) error
}

// sandboxProfileBuilders abstracts the docker-facing operations: availability
// probing and image builds. Production wires the real docker binary; tests
// stub both.
type sandboxProfileBuilders interface {
	ImageAvailable(ctx context.Context, ref string) bool
	Build(ctx context.Context, def sandbox.ProfileDefinition) (string, error)
}

type dockerProfileBuilder struct {
	binary string
}

func (b dockerProfileBuilder) ImageAvailable(ctx context.Context, ref string) bool {
	return sandbox.ImageAvailable(ctx, b.binary, ref)
}

func (b dockerProfileBuilder) Build(ctx context.Context, def sandbox.ProfileDefinition) (string, error) {
	return sandbox.BuildDefinition(ctx, b.binary, def)
}

func sandboxDockerBinary() string {
	if v := os.Getenv("KERNEL_DOCKER_BINARY"); v != "" {
		return v
	}
	return "docker"
}

// sandboxProfileView is the API shape: a registry row plus the local
// availability flag (the image exists in the docker store — --pull=never
// runs require it).
type sandboxProfileView struct {
	workspace.SandboxProfile
	Available bool `json:"available"`
}

func emitSandboxProfileEvent(ctx context.Context, events sandboxProfileOutbox, eventType, profileID string, data map[string]any) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("sandbox-profile/%s/%s/%d", profileID, eventType, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "workspace", Type: "system"}},
		Type:       eventType,
		Data:       data,
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit sandbox profile event", "profile", profileID, "type", eventType, "error", err)
	}
}

func writeSandboxProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		writeError(w, 404, err)
	case errors.Is(err, workspace.ErrProfileBuiltin):
		writeError(w, 409, err)
	case errors.Is(err, workspace.ErrProfileInUse):
		writeError(w, 409, err)
	default:
		writeError(w, 400, err)
	}
}

// RegisterSandboxProfileRoutes mounts the profile registry API.
func RegisterSandboxProfileRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws sandboxProfileStore, events sandboxProfileOutbox, builders sandboxProfileBuilders) {
	mux.HandleFunc("GET /v1/workspace/sandbox-profiles", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		profiles, err := ws.ListSandboxProfiles(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		views := make([]sandboxProfileView, 0, len(profiles))
		for _, p := range profiles {
			available := p.Image != "" && builders.ImageAvailable(r.Context(), p.Image)
			views = append(views, sandboxProfileView{SandboxProfile: p, Available: available})
		}
		writeJSON(w, 200, map[string]any{"profiles": views})
	})

	mux.HandleFunc("POST /v1/workspace/sandbox-profiles", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var p workspace.SandboxProfile
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := ws.CreateSandboxProfile(r.Context(), &p); err != nil {
			writeSandboxProfileError(w, err)
			return
		}
		actor := requestSubject(r)
		_ = ws.RecordAccessAudit(r.Context(), actor, workspace.AuditSandboxProfileCreated, "sandbox_profile", p.ID, map[string]any{"image": p.Image})
		emitSandboxProfileEvent(r.Context(), events, "sandbox.profile.created", p.ID, map[string]any{"profile": p.ID, "by": actor, "image": p.Image})
		writeJSON(w, 201, p)
	})

	mux.HandleFunc("PUT /v1/workspace/sandbox-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		var p workspace.SandboxProfile
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
			writeError(w, 400, err)
			return
		}
		p.ID = r.PathValue("id")
		if err := ws.UpdateSandboxProfile(r.Context(), p); err != nil {
			writeSandboxProfileError(w, err)
			return
		}
		actor := requestSubject(r)
		_ = ws.RecordAccessAudit(r.Context(), actor, workspace.AuditSandboxProfileUpdated, "sandbox_profile", p.ID, map[string]any{"image": p.Image, "memory": p.Memory, "cpus": p.CPUs})
		emitSandboxProfileEvent(r.Context(), events, "sandbox.profile.updated", p.ID, map[string]any{"profile": p.ID, "by": actor, "image": p.Image, "memory": p.Memory, "cpus": p.CPUs})
		// Respond with the stored row: a builtin PUT carries only resource
		// overrides, the merged identity lives in the store.
		updated, err := ws.GetSandboxProfile(r.Context(), p.ID)
		if err != nil {
			writeSandboxProfileError(w, err)
			return
		}
		writeJSON(w, 200, updated)
	})

	mux.HandleFunc("DELETE /v1/workspace/sandbox-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		id := r.PathValue("id")
		if err := ws.DeleteSandboxProfile(r.Context(), id); err != nil {
			writeSandboxProfileError(w, err)
			return
		}
		actor := requestSubject(r)
		_ = ws.RecordAccessAudit(r.Context(), actor, workspace.AuditSandboxProfileDeleted, "sandbox_profile", id, nil)
		emitSandboxProfileEvent(r.Context(), events, "sandbox.profile.deleted", id, map[string]any{"profile": id, "by": actor})
		writeJSON(w, 200, map[string]any{"deleted": id})
	})

	mux.HandleFunc("POST /v1/workspace/sandbox-profiles/{id}/build", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		id := r.PathValue("id")
		def, ok := sandbox.ProfileDefinitions()[id]
		if !ok {
			// Custom profiles reference an external image; there is nothing to
			// build locally.
			writeError(w, 409, fmt.Errorf("profile %q has no builtin build recipe; set its image reference directly", id))
			return
		}
		ref, err := builders.Build(r.Context(), def)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if err := ws.SetSandboxProfileImage(r.Context(), id, ref); err != nil {
			writeSandboxProfileError(w, err)
			return
		}
		actor := requestSubject(r)
		_ = ws.RecordAccessAudit(r.Context(), actor, workspace.AuditSandboxProfileBuilt, "sandbox_profile", id, map[string]any{"image": ref})
		emitSandboxProfileEvent(r.Context(), events, "sandbox.profile.built", id, map[string]any{"profile": id, "image": ref, "by": actor})
		updated, err := ws.GetSandboxProfile(r.Context(), id)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, updated)
	})
}
