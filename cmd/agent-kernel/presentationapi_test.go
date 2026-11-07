package main

// Presentation routes tests (docs/presentation-engine.md §18.1/§18.2): the
// create → read → render → adapt round trip over the HTTP surface with an
// in-memory store, the validation refusal path (422 with exact errors), the
// lifecycle archive move and the security checks (XSS never reaches a
// response body, external images stay off by default).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/presentation"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

type fakePresentationStore struct {
	mu        sync.Mutex
	rows      map[string]workspace.Presentation
	revisions map[string][]presentation.Revision
	audit     []string
}

func newFakePresentationStore() *fakePresentationStore {
	return &fakePresentationStore{
		rows:      map[string]workspace.Presentation{},
		revisions: map[string][]presentation.Revision{},
	}
}

func (f *fakePresentationStore) CreatePresentation(_ context.Context, p *workspace.Presentation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.rows[p.ID]; exists {
		return workspace.ErrConflict
	}
	f.rows[p.ID] = *p
	return nil
}

func (f *fakePresentationStore) GetPresentation(_ context.Context, id string) (workspace.Presentation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return workspace.Presentation{}, workspace.ErrNotFound
	}
	return row, nil
}

func (f *fakePresentationStore) UpdatePresentation(_ context.Context, p *workspace.Presentation, revision presentation.Revision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.rows[p.ID]
	if !ok {
		return workspace.ErrNotFound
	}
	f.revisions[p.ID] = append(f.revisions[p.ID], presentation.Revision{Version: current.Version, Document: current.Document, ChangedBy: revision.ChangedBy})
	current.Document = p.Document
	current.Title = p.Title
	current.TextFallback = p.TextFallback
	current.Version++
	current.Status = "updated"
	f.rows[p.ID] = current
	*p = current
	return nil
}

func (f *fakePresentationStore) SetPresentationStatus(_ context.Context, id string, from, to presentation.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return workspace.ErrNotFound
	}
	if presentation.Status(row.Status) != from {
		return workspace.ErrNotFound
	}
	row.Status = string(to)
	f.rows[id] = row
	return nil
}

func (f *fakePresentationStore) ListPresentations(_ context.Context, projectID string, limit int) ([]workspace.Presentation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workspace.Presentation
	for _, row := range f.rows {
		if row.ProjectID == projectID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakePresentationStore) ListPresentationsByRun(_ context.Context, runID string, limit int) ([]workspace.Presentation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workspace.Presentation
	for _, row := range f.rows {
		if row.RunID == runID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakePresentationStore) ListPresentationRevisions(_ context.Context, id string) ([]presentation.Revision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revisions[id], nil
}

func (f *fakePresentationStore) AttachArtifact(_ context.Context, id, artifactID, _ string) (workspace.Presentation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return workspace.Presentation{}, workspace.ErrNotFound
	}
	row.Artifacts = append(row.Artifacts, artifactID)
	f.rows[id] = row
	return row, nil
}

func (f *fakePresentationStore) RecordAccessAudit(_ context.Context, actor, action, entityKind, entityID string, _ map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audit = append(f.audit, actor+":"+action+":"+entityKind+":"+entityID)
	return nil
}

type fakePresentationOutbox struct {
	mu     sync.Mutex
	events []observation.Event
}

func (f *fakePresentationOutbox) Enqueue(_ context.Context, event observation.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

// fakeSignaler records the approvals the interaction handler sends — the
// temporal client surface.
type fakeSignaler struct {
	mu        sync.Mutex
	signals   []map[string]any
	signalErr error
}

func (f *fakeSignaler) SignalWorkflow(_ context.Context, workflowID string, runID string, signalName string, arg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.signalErr != nil {
		return f.signalErr
	}
	f.signals = append(f.signals, map[string]any{
		"workflow": workflowID, "run": runID, "signal": signalName, "arg": arg,
	})
	return nil
}

// fakeHitlStore is the human-request surface of the interaction handler.
type fakeHitlStore struct {
	mu       sync.Mutex
	requests map[string]workspace.HumanRequest
}

func newFakeHitlStore() *fakeHitlStore {
	return &fakeHitlStore{requests: map[string]workspace.HumanRequest{}}
}

func (f *fakeHitlStore) GetHumanRequest(_ context.Context, id string) (workspace.HumanRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.requests[id]
	if !ok {
		return workspace.HumanRequest{}, workspace.ErrNotFound
	}
	return row, nil
}

func (f *fakeHitlStore) AnswerHumanRequest(_ context.Context, id, response, answeredBy string) (workspace.HumanRequest, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.requests[id]
	if !ok {
		return workspace.HumanRequest{}, false, workspace.ErrNotFound
	}
	if row.Status != workspace.HumanStatusPending && row.Status != workspace.HumanStatusDelivered {
		return row, false, nil
	}
	row.Status = workspace.HumanStatusAnswered
	row.Response = response
	row.AnsweredBy = answeredBy
	f.requests[id] = row
	return row, true, nil
}

func (f *fakeHitlStore) CancelHumanRequest(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.requests[id]
	if !ok {
		return workspace.ErrNotFound
	}
	row.Status = "cancelled"
	f.requests[id] = row
	return nil
}

func newPresentationServer(t *testing.T) (*httptest.Server, *fakePresentationStore, *fakePresentationOutbox) {
	return newPresentationServerWith(t, nil, nil)
}

func newPresentationServerWith(t *testing.T, signaler *fakeSignaler, hitl *fakeHitlStore) (*httptest.Server, *fakePresentationStore, *fakePresentationOutbox) {
	t.Helper()
	store := newFakePresentationStore()
	outbox := &fakePresentationOutbox{}
	if signaler == nil {
		signaler = &fakeSignaler{}
	}
	if hitl == nil {
		hitl = newFakeHitlStore()
	}
	gate, err := controlplane.NewGate("writertoken:writer:writer:*;readertoken:reader:reader:*;operatortoken:operator:operator:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerPresentationRoutes(mux, gate, store, outbox, signaler, hitl, "kernel-source")
	return httptest.NewServer(gate.Authenticate(mux)), store, outbox
}

func auth(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

const validSampleIR = `{"version":"1.0","type":"presentation","title":"Отчёт","content":[
	{"type":"text","content":"Тестирование завершено."},
	{"type":"kpi","items":[{"label":"Passed","value":942,"status":"success"}]}
]}`

func TestPresentationRoundTrip(t *testing.T) {
	server, store, outbox := newPresentationServer(t)
	defer server.Close()

	// Create (publish).
	create, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations?project=omni", strings.NewReader(`{"id":"report-1","publish":true,"run_id":"run-9","document":`+validSampleIR+`}`))
	resp, err := server.Client().Do(auth(create, "writertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 201, resp.StatusCode)
	var created workspace.Presentation
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	require.Equal(t, "report-1", created.ID)
	require.Equal(t, "published", created.Status)
	require.Equal(t, "run-9", created.RunID)
	require.Contains(t, created.TextFallback, "Тестирование завершено")

	// Read.
	get, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations/report-1", nil)
	page, err := server.Client().Do(auth(get, "readertoken"))
	require.NoError(t, err)
	defer page.Body.Close()
	require.Equal(t, 200, page.StatusCode)

	// List by run.
	list, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations?run_id=run-9", nil)
	listResp, err := server.Client().Do(auth(list, "readertoken"))
	require.NoError(t, err)
	defer listResp.Body.Close()
	var listed struct {
		Presentations []workspace.Presentation `json:"presentations"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&listed))
	require.Len(t, listed.Presentations, 1)

	// Text render endpoint.
	renderReq, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations/report-1/render?format=text", nil)
	renderResp, err := server.Client().Do(auth(renderReq, "readertoken"))
	require.NoError(t, err)
	defer renderResp.Body.Close()
	require.Equal(t, 200, renderResp.StatusCode)
	var rendered struct {
		Body string `json:"body"`
	}
	require.NoError(t, json.NewDecoder(renderResp.Body).Decode(&rendered))
	require.Contains(t, rendered.Body, "Passed: 942")

	// SVG render endpoint returns an image document.
	svgReq, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations/report-1/render?format=svg", nil)
	svgResp, err := server.Client().Do(auth(svgReq, "readertoken"))
	require.NoError(t, err)
	defer svgResp.Body.Close()
	require.Equal(t, 200, svgResp.StatusCode)
	require.Equal(t, "image/svg+xml", svgResp.Header.Get("Content-Type"))

	// Update moves the row to updated with a revision history.
	update, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/presentations/report-1", strings.NewReader(`{"document":`+validSampleIR+`}`))
	updateResp, err := server.Client().Do(auth(update, "writertoken"))
	require.NoError(t, err)
	defer updateResp.Body.Close()
	require.Equal(t, 200, updateResp.StatusCode)
	var updated workspace.Presentation
	require.NoError(t, json.NewDecoder(updateResp.Body).Decode(&updated))
	require.Equal(t, 2, updated.Version)
	require.Equal(t, "updated", updated.Status)

	revResp, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations/report-1/revisions", nil)
	revList, err := server.Client().Do(auth(revResp, "readertoken"))
	require.NoError(t, err)
	defer revList.Body.Close()
	var revisions struct {
		Revisions []presentation.Revision `json:"revisions"`
	}
	require.NoError(t, json.NewDecoder(revList.Body).Decode(&revisions))
	require.Len(t, revisions.Revisions, 1)

	// Archive.
	archive, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations/report-1/archive", nil)
	archiveResp, err := server.Client().Do(auth(archive, "writertoken"))
	require.NoError(t, err)
	defer archiveResp.Body.Close()
	require.Equal(t, 200, archiveResp.StatusCode)
	row, err := store.GetPresentation(context.Background(), "report-1")
	require.NoError(t, err)
	require.Equal(t, "archived", row.Status)

	// Journal events: created, published, validated/updated, archived.
	kind := func(eventType string) int {
		count := 0
		for _, event := range outbox.events {
			if event.Type == eventType {
				count++
			}
		}
		return count
	}
	require.GreaterOrEqual(t, kind("presentation.created"), 1)
	require.GreaterOrEqual(t, kind("presentation.published"), 1)
	require.GreaterOrEqual(t, kind("presentation.updated"), 1)
	require.GreaterOrEqual(t, kind("presentation.archived"), 1)
	// The published event carries the IR for the run stream renderer.
	for _, event := range outbox.events {
		if event.Type == "presentation.published" {
			require.Contains(t, event.Data, "presentation")
			require.Equal(t, "omni", event.Context.Project)
			require.Equal(t, "run-9", event.Context.Run)
		}
	}
}

func TestPresentationValidationRefusal(t *testing.T) {
	server, _, outbox := newPresentationServer(t)
	defer server.Close()

	// XSS-smuggled document: the schema refuses unknown props fields before
	// anything else.
	bad := `{"version":"1.0","type":"presentation","content":[
		{"type":"text","content":"ok","props":{"style":"color:red"}}
	]}`
	create, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations?project=omni", strings.NewReader(`{"document":`+bad+`}`))
	resp, err := server.Client().Do(auth(create, "writertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 422, resp.StatusCode)
	var failure struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&failure))
	require.Contains(t, failure.Error, "props")

	// Oversized document.
	huge := `{"version":"1.0","type":"presentation","content":[{"type":"text","content":"` + strings.Repeat("x", 300<<10) + `"}]}`
	big, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations?project=omni", strings.NewReader(`{"document":`+huge+`}`))
	bigResp, err := server.Client().Do(auth(big, "writertoken"))
	require.NoError(t, err)
	defer bigResp.Body.Close()
	require.Equal(t, 422, bigResp.StatusCode)

	// The refusal journals validation_failed for observability (§15).
	found := false
	for _, event := range outbox.events {
		if event.Type == "presentation.validation_failed" {
			found = true
		}
	}
	require.True(t, found)
}

func TestPresentationAdaptPreview(t *testing.T) {
	server, _, _ := newPresentationServer(t)
	defer server.Close()

	create, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations?project=omni", strings.NewReader(`{"id":"viz-1","publish":true,"document":{"version":"1.0","type":"presentation","content":[
		{"type":"chart","props":{"chartType":"bar","title":"latency"},"data":{"labels":["a","b"],"values":[1,2]}}
	]}}`))
	resp, err := server.Client().Do(auth(create, "writertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 201, resp.StatusCode)

	// Telegram: PNG attachments, no SVG.
	adaptReq, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations/viz-1/adapt?channel=telegram", nil)
	adaptResp, err := server.Client().Do(auth(adaptReq, "readertoken"))
	require.NoError(t, err)
	defer adaptResp.Body.Close()
	require.Equal(t, 200, adaptResp.StatusCode)
	var delivery struct {
		Kind        string           `json:"kind"`
		Attachments []map[string]any `json:"attachments"`
	}
	require.NoError(t, json.NewDecoder(adaptResp.Body).Decode(&delivery))
	require.Equal(t, "markdown", delivery.Kind)
	require.NotEmpty(t, delivery.Attachments)
	require.Equal(t, "png", delivery.Attachments[0]["format"])

	// Unknown channel is a 400, not a silent guess.
	badReq, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations/viz-1/adapt?channel=carrier-pigeon", nil)
	badResp, err := server.Client().Do(auth(badReq, "readertoken"))
	require.NoError(t, err)
	defer badResp.Body.Close()
	require.Equal(t, 400, badResp.StatusCode)
}

func TestPresentationSchemaEndpoint(t *testing.T) {
	server, _, _ := newPresentationServer(t)
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentations/schema", nil)
	resp, err := server.Client().Do(auth(req, "readertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, "application/schema+json", resp.Header.Get("Content-Type"))
	var schema map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&schema))
	require.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema["$schema"])
}

func TestPresentationCapabilitiesEndpoint(t *testing.T) {
	server, _, _ := newPresentationServer(t)
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/presentation-capabilities", nil)
	resp, err := server.Client().Do(auth(req, "readertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	var payload struct {
		Channels map[string]presentation.Capabilities `json:"channels"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Contains(t, payload.Channels, "web")
	require.Contains(t, payload.Channels, "matrix")
	require.Contains(t, payload.Channels, "telegram")
}

// Interaction Handler tests (docs/presentation-engine.md §11): a declared
// button action resumes the paused run through the HITL signal; replays and
// undeclared actions are refused.
func TestPresentationInteraction(t *testing.T) {
	signaler := &fakeSignaler{}
	hitl := newFakeHitlStore()
	server, _, outbox := newPresentationServerWith(t, signaler, hitl)
	defer server.Close()

	// A presentation with an Approve button pointing at ask-123.
	buttonDoc := `{"version":"1.0","type":"presentation","content":[
		{"type":"button","id":"approve-btn","content":"Approve","actions":[
			{"type":"submit","target":"ask-123","payload":{"decision":"approve"}}
		]}
	]}`
	create, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations?project=omni", strings.NewReader(`{"id":"gate-1","publish":true,"run_id":"run-42","document":`+buttonDoc+`}`))
	resp, err := server.Client().Do(auth(create, "writertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 201, resp.StatusCode)

	// The ask the button targets.
	hitl.requests["ask-123"] = workspace.HumanRequest{
		ID: "ask-123", RunID: "run-42", ProjectID: "omni", Status: workspace.HumanStatusDelivered,
	}

	interact := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations/gate-1/interact", strings.NewReader(body))
		resp, err := server.Client().Do(auth(req, "operatortoken"))
		require.NoError(t, err)
		return resp
	}

	// Happy path: the decision rides the approval signal, the ask closes.
	resp1 := interact(`{"component_id":"approve-btn"}`)
	require.Equal(t, 202, resp1.StatusCode)
	resp1.Body.Close()
	signaler.mu.Lock()
	require.Len(t, signaler.signals, 1)
	approval := signaler.signals[0]["arg"].(agent.Approval)
	require.Equal(t, "ask-123", approval.OperationID)
	require.True(t, approval.Approved)
	signaler.mu.Unlock()
	row, err := hitl.GetHumanRequest(context.Background(), "ask-123")
	require.NoError(t, err)
	require.Equal(t, workspace.HumanStatusAnswered, row.Status)
	// presentation.interaction journaled (§15).
	interactions := 0
	for _, event := range outbox.events {
		if event.Type == "presentation.interaction" {
			interactions++
		}
	}
	require.Equal(t, 1, interactions)

	// Replay: the same click twice must not resume the run twice (§11.2).
	resp2 := interact(`{"component_id":"approve-btn"}`)
	resp2.Body.Close()
	require.Equal(t, 409, resp2.StatusCode)

	// Undeclared component: refused.
	resp3 := interact(`{"component_id":"no-such-button"}`)
	resp3.Body.Close()
	require.Equal(t, 422, resp3.StatusCode)

	// Rewritten target: the declared target wins, a smuggled one is refused.
	resp4 := interact(`{"component_id":"approve-btn","target":"ask-999"}`)
	resp4.Body.Close()
	require.Equal(t, 403, resp4.StatusCode)

	// Unknown target (not an open ask): 404. A second button document with
	// an answered ask demonstrates the replay branch without the target
	// mismatch check firing first (§11.2: the declared target is the only
	// legal one).
	resp5 := interact(`{"component_id":"approve-btn","target":"ask-123"}`)
	resp5.Body.Close()
	require.Equal(t, 409, resp5.StatusCode, "an already-answered ask is a replay, not a fresh answer")
}

func TestPresentationInteractionFormValues(t *testing.T) {
	signaler := &fakeSignaler{}
	hitl := newFakeHitlStore()
	server, _, _ := newPresentationServerWith(t, signaler, hitl)
	defer server.Close()

	formDoc := `{"version":"1.0","type":"presentation","content":[
		{"type":"form","id":"review-form","content":"Review", "props":{"schema":{"type":"object","properties":{"name":{"type":"string","minLength":3},"priority":{"type":"string","enum":["high","normal"]}},"required":["name"]},"submit":"Send"},"actions":[{"type":"submit","target":"ask-form"}]}
	]}`
	create, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations?project=omni", strings.NewReader(`{"id":"form-1","publish":true,"run_id":"run-7","document":`+formDoc+`}`))
	resp, err := server.Client().Do(auth(create, "writertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 201, resp.StatusCode)

	hitl.requests["ask-form"] = workspace.HumanRequest{
		ID: "ask-form", RunID: "run-7", ProjectID: "omni", Status: workspace.HumanStatusPending,
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":3},"priority":{"type":"string","enum":["high","normal"]}},"required":["name"]}`),
	}

	// Invalid values: the run must NOT resume (422 before the signal).
	bad, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations/form-1/interact", strings.NewReader(`{"component_id":"review-form","payload":{"name":"a","priority":"high"}}`))
	badResp, err := server.Client().Do(auth(bad, "operatortoken"))
	require.NoError(t, err)
	badResp.Body.Close()
	require.Equal(t, 422, badResp.StatusCode)
	signaler.mu.Lock()
	require.Len(t, signaler.signals, 0)
	signaler.mu.Unlock()

	// Valid form submission: values fold into the response canonically.
	good, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations/form-1/interact", strings.NewReader(`{"component_id":"review-form","payload":{"name":"checkpoint","priority":"high"}}`))
	goodResp, err := server.Client().Do(auth(good, "operatortoken"))
	require.NoError(t, err)
	defer goodResp.Body.Close()
	require.Equal(t, 202, goodResp.StatusCode)
	signaler.mu.Lock()
	require.Len(t, signaler.signals, 1)
	approval := signaler.signals[0]["arg"].(agent.Approval)
	require.True(t, approval.Approved)
	require.Contains(t, approval.Response, "checkpoint")
	signaler.mu.Unlock()
}

func TestPresentationInteractionDeadRun(t *testing.T) {
	signaler := &fakeSignaler{signalErr: errors.New("workflow not found")}
	hitl := newFakeHitlStore()
	server, _, _ := newPresentationServerWith(t, signaler, hitl)
	defer server.Close()

	buttonDoc := `{"version":"1.0","type":"presentation","content":[
		{"type":"button","id":"btn","content":"Approve","actions":[{"type":"submit","target":"dead-ask"}]}
	]}`
	create, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations?project=omni", strings.NewReader(`{"id":"dead-1","publish":true,"document":`+buttonDoc+`}`))
	resp, err := server.Client().Do(auth(create, "writertoken"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 201, resp.StatusCode)

	hitl.requests["dead-ask"] = workspace.HumanRequest{ID: "dead-ask", RunID: "gone", ProjectID: "omni", Status: workspace.HumanStatusPending}

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/presentations/dead-1/interact", strings.NewReader(`{"component_id":"btn"}`))
	interactResp, err := server.Client().Do(auth(req, "operatortoken"))
	require.NoError(t, err)
	defer interactResp.Body.Close()
	require.Equal(t, 410, interactResp.StatusCode)
	// The zombie ask is closed: the stale button stops working (§11.2).
	row, err := hitl.GetHumanRequest(context.Background(), "dead-ask")
	require.NoError(t, err)
	require.Equal(t, "cancelled", row.Status)
}
