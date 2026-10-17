package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// fakeSandboxProfileStore satisfies the registry router's storage surface.
type fakeSandboxProfileStore struct {
	mu       sync.Mutex
	profiles map[string]workspace.SandboxProfile
	audits   []storedAuditCall
}

type storedAuditCall struct {
	action string
	id     string
}

func newFakeSandboxProfileStore(profiles ...workspace.SandboxProfile) *fakeSandboxProfileStore {
	byID := map[string]workspace.SandboxProfile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}
	return &fakeSandboxProfileStore{profiles: byID}
}

func (f *fakeSandboxProfileStore) ListSandboxProfiles(context.Context) ([]workspace.SandboxProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []workspace.SandboxProfile
	for _, p := range f.profiles {
		result = append(result, p)
	}
	return result, nil
}

func (f *fakeSandboxProfileStore) GetSandboxProfile(_ context.Context, id string) (workspace.SandboxProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.profiles[id]; ok {
		return p, nil
	}
	return workspace.SandboxProfile{}, workspace.ErrNotFound
}

func (f *fakeSandboxProfileStore) CreateSandboxProfile(_ context.Context, p *workspace.SandboxProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, builtin := range workspace.BuiltinSandboxProfiles() {
		if p.ID == builtin.ID {
			return fmt.Errorf("%w: %s", workspace.ErrProfileBuiltin, p.ID)
		}
	}
	if err := workspace.ValidateSandboxProfile(*p); err != nil {
		return err
	}
	f.profiles[p.ID] = *p
	return nil
}

func (f *fakeSandboxProfileStore) UpdateSandboxProfile(_ context.Context, p workspace.SandboxProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.profiles[p.ID]
	if !ok {
		return workspace.ErrNotFound
	}
	if current.Builtin {
		// Mirrors the real store: a builtin PUT only carries resource
		// overrides; identity and image come from the stored row.
		p.Name = current.Name
		p.Description = current.Description
		p.Toolchains = current.Toolchains
		p.Image = current.Image
		p.Builtin = true
	} else {
		p.Builtin = false
	}
	if err := workspace.ValidateSandboxProfile(p); err != nil {
		return err
	}
	f.profiles[p.ID] = p
	return nil
}

func (f *fakeSandboxProfileStore) SetSandboxProfileImage(_ context.Context, id, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.profiles[id]
	if !ok {
		return workspace.ErrNotFound
	}
	if !current.Builtin {
		return errors.New("not builtin")
	}
	if !workspace.IsDigestPinnedImage(image) {
		return errors.New("not digest pinned")
	}
	current.Image = image
	f.profiles[id] = current
	return nil
}

func (f *fakeSandboxProfileStore) SetCustomProfileImage(_ context.Context, id, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.profiles[id]
	if !ok {
		return workspace.ErrNotFound
	}
	if current.Builtin {
		return errors.New("not custom")
	}
	if current.Base == "" {
		return errors.New("no base recipe")
	}
	if !workspace.IsDigestPinnedImage(image) {
		return errors.New("not digest pinned")
	}
	current.Image = image
	f.profiles[id] = current
	return nil
}

func (f *fakeSandboxProfileStore) SetExternalProfileImage(_ context.Context, id, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.profiles[id]
	if !ok {
		return workspace.ErrNotFound
	}
	if current.Builtin {
		return errors.New("not custom")
	}
	if current.Base != "" {
		return errors.New("recipe-based profile must be built, not pulled")
	}
	if !workspace.IsDigestPinnedImage(image) {
		return errors.New("not digest pinned")
	}
	current.Image = image
	f.profiles[id] = current
	return nil
}

func (f *fakeSandboxProfileStore) DeleteSandboxProfile(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.profiles[id]
	if !ok {
		return workspace.ErrNotFound
	}
	if current.Builtin {
		return fmt.Errorf("%w: %s", workspace.ErrProfileBuiltin, id)
	}
	delete(f.profiles, id)
	return nil
}

func (f *fakeSandboxProfileStore) RecordAccessAudit(_ context.Context, actor, action, entityKind, entityID string, _ map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, storedAuditCall{action: action, id: entityID})
	return nil
}

// fakeProfileBuilders stubs the docker surface: builds return a fixed digest.
type fakeProfileBuilders struct {
	available map[string]bool
	buildErr  error
}

func (b fakeProfileBuilders) ImageAvailable(_ context.Context, ref string) bool {
	return b.available[ref]
}

func (b fakeProfileBuilders) Build(context.Context, sandbox.ProfileDefinition) (string, error) {
	if b.buildErr != nil {
		return "", b.buildErr
	}
	return "temporality-sandbox-test@sha256:" + strings.Repeat("cd", 32), nil
}

func (b fakeProfileBuilders) Pull(_ context.Context, reference string) (string, error) {
	if b.buildErr != nil {
		return "", b.buildErr
	}
	// The pinned form mirrors a real pull: whatever the input, the answer is
	// a digest-pinned reference.
	repo := reference
	if idx := strings.IndexAny(repo, ":@"); idx > 0 {
		repo = repo[:idx]
	}
	return repo + "@sha256:" + strings.Repeat("ef", 32), nil
}

type captureProfileOutbox struct {
	mu     sync.Mutex
	events []observation.Event
}

func (c *captureProfileOutbox) Enqueue(_ context.Context, e observation.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func (c *captureProfileOutbox) byType(eventType string) []observation.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var matched []observation.Event
	for _, e := range c.events {
		if e.Type == eventType {
			matched = append(matched, e)
		}
	}
	return matched
}

func newSandboxProfileServer(t *testing.T, store *fakeSandboxProfileStore, outbox *captureProfileOutbox, builders sandboxProfileBuilders) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*\nadmintoken:admin:admin:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterSandboxProfileRoutes(mux, gate, store, outbox, builders)
	return httptest.NewServer(gate.Authenticate(mux))
}

func profileRequest(t *testing.T, server *httptest.Server, method, token, path, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

func builtinProfileFixture() workspace.SandboxProfile {
	for _, p := range workspace.BuiltinSandboxProfiles() {
		if p.ID == "node" {
			return p
		}
	}
	panic("node builtin missing")
}

// The workspace builtin catalog and the kernel build recipes are two sides of
// one catalog: every builtin profile must have a definition and vice versa.
func TestBuiltinCatalogsStayInSync(t *testing.T) {
	definitions := sandbox.ProfileDefinitions()
	for _, p := range workspace.BuiltinSandboxProfiles() {
		require.Contains(t, definitions, p.ID, "workspace builtin %s has no build recipe", p.ID)
	}
	for id, def := range definitions {
		require.NotEmpty(t, def.Dockerfile)
		require.Equal(t, id, def.Profile)
		var found bool
		for _, p := range workspace.BuiltinSandboxProfiles() {
			if p.ID == id {
				found = true
			}
		}
		require.True(t, found, "kernel recipe %s has no workspace builtin", id)
	}
}

// List enriches each row with local availability of the digest-pinned image.
func TestListProfilesReportsAvailability(t *testing.T) {
	node := builtinProfileFixture()
	node.Image = "temporality-sandbox-node@sha256:" + strings.Repeat("aa", 32)
	browser := workspace.SandboxProfile{ID: "browser-e2e", Name: "Browser", Builtin: true} // unbuilt
	store := newFakeSandboxProfileStore(node, browser)
	server := newSandboxProfileServer(t, store, &captureProfileOutbox{}, fakeProfileBuilders{available: map[string]bool{node.Image: true}})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodGet, "readertoken", "/v1/workspace/sandbox-profiles", "")
	require.Equal(t, 200, status)
	var payload struct {
		Profiles []struct {
			ID        string `json:"id"`
			Available bool   `json:"available"`
			Image     string `json:"image"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	byID := map[string]bool{}
	for _, p := range payload.Profiles {
		byID[p.ID] = p.Available
	}
	require.True(t, byID["node"])
	require.False(t, byID["browser-e2e"], "unbuilt profile must report unavailable")
}

// Building a builtin pins the digest, audits the action and journals the event.
func TestBuildBuiltinPinsImageAndAudits(t *testing.T) {
	store := newFakeSandboxProfileStore(builtinProfileFixture())
	outbox := &captureProfileOutbox{}
	server := newSandboxProfileServer(t, store, outbox, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/node/build", "")
	require.Equal(t, 200, status, body)
	require.Contains(t, body, "temporality-sandbox-test@sha256:")

	built := outbox.byType("sandbox.profile.built")
	require.Len(t, built, 1)
	require.Equal(t, "node", built[0].Data["profile"])
	require.Len(t, store.audits, 1)
	require.Equal(t, workspace.AuditSandboxProfileBuilt, store.audits[0].action)
}

// Building a recipe-based custom profile composes the definition, pins the
// digest into the row and journals the event.
func TestBuildCustomRecipePinsImage(t *testing.T) {
	custom := workspace.SandboxProfile{ID: "qa-tools", Name: "QA tools", Base: "python", Packages: []string{"gcc", "linux-headers"}}
	store := newFakeSandboxProfileStore(builtinProfileFixture(), custom)
	outbox := &captureProfileOutbox{}
	server := newSandboxProfileServer(t, store, outbox, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/qa-tools/build", "")
	require.Equal(t, 200, status, body)
	require.Contains(t, body, "temporality-sandbox-test@sha256:")

	built := outbox.byType("sandbox.profile.built")
	require.Len(t, built, 1)
	require.Equal(t, "qa-tools", built[0].Data["profile"])

	pinned, err := store.GetSandboxProfile(context.Background(), "qa-tools")
	require.NoError(t, err)
	require.Equal(t, "temporality-sandbox-test@sha256:"+strings.Repeat("cd", 32), pinned.Image)
	require.Equal(t, "python", pinned.Base)
}

// Recipe creation and update through the API: valid recipes save, malformed
// ones are rejected, recipe changes invalidate the pinned image.
func TestCustomRecipeLifecycle(t *testing.T) {
	store := newFakeSandboxProfileStore(builtinProfileFixture())
	server := newSandboxProfileServer(t, store, &captureProfileOutbox{}, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"qa-tools","name":"QA tools","base":"python","packages":["gcc"]}`)
	require.Equal(t, 201, status, body)

	// Building then editing the recipe drops the stale digest.
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/qa-tools/build", "")
	require.Equal(t, 200, status)
	status, _ = profileRequest(t, server, http.MethodPut, "writertoken", "/v1/workspace/sandbox-profiles/qa-tools",
		`{"id":"qa-tools","name":"QA tools","base":"python","packages":["gcc","linux-headers"]}`)
	require.Equal(t, 200, status)
	updated, err := store.GetSandboxProfile(context.Background(), "qa-tools")
	require.NoError(t, err)
	require.Empty(t, updated.Image, "recipe change must invalidate the pinned image")

	// Base and image are mutually exclusive; unknown base and shell-bearing
	// package names are rejected.
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"both","name":"Both","base":"node","image":"repo@sha256:`+strings.Repeat("ab", 32)+`"}`)
	require.Equal(t, 400, status)
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"invented","name":"Invented","base":"invented-base"}`)
	require.Equal(t, 400, status)
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"shelly","name":"Shelly","base":"python","packages":["gcc; rm -rf /"]}`)
	require.Equal(t, 400, status)
}

// Reader cannot build; a custom profile without a recipe has nothing to build.
func TestBuildGuards(t *testing.T) {
	custom := workspace.SandboxProfile{ID: "my-toolchain", Name: "Mine", Image: "repo@sha256:" + strings.Repeat("ab", 32)}
	store := newFakeSandboxProfileStore(builtinProfileFixture(), custom)
	server := newSandboxProfileServer(t, store, &captureProfileOutbox{}, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "readertoken", "/v1/workspace/sandbox-profiles/node/build", "")
	require.Equal(t, 403, status, body)

	status, body = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/my-toolchain/build", "")
	require.Equal(t, 409, status, body)
}

// Custom profiles are creatable; builtin ids and malformed payloads are not.
func TestCreateCustomProfile(t *testing.T) {
	store := newFakeSandboxProfileStore(builtinProfileFixture())
	outbox := &captureProfileOutbox{}
	server := newSandboxProfileServer(t, store, outbox, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"rust","name":"Rust","toolchains":["rust"],"image":"repo/rust@sha256:`+strings.Repeat("ab", 32)+`"}`)
	require.Equal(t, 201, status, body)
	require.Len(t, outbox.byType("sandbox.profile.created"), 1)

	status, body = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"node","name":"Clone"}`)
	require.Equal(t, 409, status, body)

	status, body = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"bad-tag","name":"Bad","image":"rust:latest"}`)
	require.Equal(t, 400, status, body)
}

// Builtin rows cannot be deleted; a PUT narrows to resource overrides and
// never rewrites catalog identity.
func TestBuiltinProfilesAreProtected(t *testing.T) {
	store := newFakeSandboxProfileStore(builtinProfileFixture())
	server := newSandboxProfileServer(t, store, &captureProfileOutbox{}, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodDelete, "writertoken", "/v1/workspace/sandbox-profiles/node", "")
	require.Equal(t, 409, status, body)

	status, body = profileRequest(t, server, http.MethodPut, "writertoken", "/v1/workspace/sandbox-profiles/node",
		`{"name":"Rename","memory":"3g","cpus":"1.5"}`)
	require.Equal(t, 200, status, body)
	require.Contains(t, body, `"memory":"3g"`)
	require.Contains(t, body, `"cpus":"1.5"`)
	require.NotContains(t, body, "Rename")

	node, err := store.GetSandboxProfile(context.Background(), "node")
	require.NoError(t, err)
	require.Equal(t, "3g", node.Memory)
	require.Equal(t, "1.5", node.CPUs)
	require.Equal(t, "Node 22", node.Name)
}

// Rebuilding a builtin pins a fresh digest without touching tuned resources.
func TestRebuildKeepsResourceOverrides(t *testing.T) {
	node := builtinProfileFixture()
	node.Memory = "3g"
	node.CacheEnv = append(node.CacheEnv, "EXTRA_CACHE=/cache/extra")
	store := newFakeSandboxProfileStore(node)
	server := newSandboxProfileServer(t, store, &captureProfileOutbox{}, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/node/build", "")
	require.Equal(t, 200, status, body)

	built, err := store.GetSandboxProfile(context.Background(), "node")
	require.NoError(t, err)
	require.Equal(t, "3g", built.Memory)
	require.Contains(t, built.CacheEnv, "EXTRA_CACHE=/cache/extra")
	require.NotEmpty(t, built.Image)
}

func TestPreviewRecipe(t *testing.T) {
	store := newFakeSandboxProfileStore(builtinProfileFixture())
	server := newSandboxProfileServer(t, store, &captureProfileOutbox{}, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"base":"python","packages":["gcc","linux-headers"]}`)
	require.Equal(t, 200, status, body)
	var payload struct {
		Dockerfile string `json:"dockerfile"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Contains(t, payload.Dockerfile, "FROM python:3.12-alpine")
	require.Contains(t, payload.Dockerfile, "apk add --no-cache gcc linux-headers")

	// Unknown base and shell-bearing packages are rejected before any Docker.
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"base":"invented","packages":[]}`)
	require.Equal(t, 422, status)
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"base":"python","packages":["gcc; rm -rf /"]}`)
	require.Equal(t, 422, status)
}

func TestPullExternalImagePinsDigest(t *testing.T) {
	external := workspace.SandboxProfile{ID: "ext-tool", Name: "External"}
	recipe := workspace.SandboxProfile{ID: "recipe-tool", Name: "Recipe", Base: "python", Packages: []string{"gcc"}}
	store := newFakeSandboxProfileStore(builtinProfileFixture(), external, recipe)
	outbox := &captureProfileOutbox{}
	server := newSandboxProfileServer(t, store, outbox, fakeProfileBuilders{})
	defer server.Close()

	// A recipe-based profile refuses the pull.
	status, _ := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/recipe-tool/pull",
		`{"reference":"repo/tool:latest"}`)
	require.Equal(t, 409, status)

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/ext-tool/pull",
		`{"reference":"registry.local/tool:1.4"}`)
	require.Equal(t, 200, status, body)
	var pinned struct {
		Image string `json:"image"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &pinned))
	require.Contains(t, pinned.Image, "registry.local/tool@sha256:")

	built := outbox.byType("sandbox.profile.built")
	require.Len(t, built, 1)
	require.Equal(t, true, built[0].Data["pulled"])
}
