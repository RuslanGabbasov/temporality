// Package httpapi serves the Temporality observation journal API: the
// universal event ingestion endpoint, knowledge projection and hint
// retrieval. Handlers carry the exact semantics they had inside the FRP
// runtime server (era 1) at the moment of extraction.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
)

type Server struct {
	store observation.Store
	gate  *controlplane.Gate
	log   *slog.Logger
	now   func() time.Time
	// projectUnits resolves a project to the org-unit chain its visibility
	// covers (the project's own units plus all their ancestors). Shared
	// org_unit-scoped knowledge is only offered to projects whose chain
	// contains the knowledge's scope unit (docs/knowledge-evolution.md §5–6).
	projectUnits func(ctx context.Context, project string) ([]string, error)
	// unitExists validates org-unit IDs on scope promotion. Both hooks are
	// optional: without them the journal serves project-local and
	// organization-scoped knowledge, but org_unit targeting is unavailable.
	unitExists func(ctx context.Context, unitID string) bool
	// topicAliases resolves the project's judge-normalized area map. Optional:
	// without it areas render with their raw topic/entity values.
	topicAliases func(ctx context.Context, project string) (map[string]AreaAlias, error)
	// humanFactors resolves the HAB estimate coefficients. Optional.
	humanFactors func(ctx context.Context) (observation.HumanFactors, error)
	// counters: per-route request accounting, read by the debug listener.
	counters *endpointCounters
	// handler: the middleware-wrapped mux, built by NewServer.
	handler http.Handler
	// knowledge projection cache (project → visible+aliased items), TTL-bound
	// and invalidated by the knowledge-mutating handlers. Guarded by its own
	// mutex: the projection is the journal's most expensive read path.
	knowledgeMu    sync.Mutex
	knowledgeCache map[string]knowledgeCacheEntry
}

// Option customizes the journal server.
type Option func(*Server)

// WithProjectUnits wires the project → org-unit-chain resolver used for
// shared-knowledge visibility. The resolver must return the project's own
// units and every ancestor; an error degrades to project-only visibility.
func WithProjectUnits(fn func(ctx context.Context, project string) ([]string, error)) Option {
	return func(s *Server) { s.projectUnits = fn }
}

// WithUnitExists wires org-unit existence validation for scope promotion.
func WithUnitExists(fn func(ctx context.Context, unitID string) bool) Option {
	return func(s *Server) { s.unitExists = fn }
}

// AreaAlias is one judge decision about a raw topic/entity value: which
// canonical area it belongs to (empty = keep as-is) or that it is not an
// area at all (hidden). Applied when aggregating areas and facets; the
// knowledge records themselves stay untouched.
type AreaAlias struct {
	Canonical string `json:"canonical,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
}

// WithTopicAliases wires the project → alias-map reader (the normalized map
// the kernel's judge sweep stores in the workspace KV). An error or a nil
// hook degrades to raw values — normalization is a lens, never a hard
// dependency.
func WithTopicAliases(fn func(ctx context.Context, project string) (map[string]AreaAlias, error)) Option {
	return func(s *Server) { s.topicAliases = fn }
}

// WithHumanFactors wires the human-attention estimate coefficients for the
// health stats (docs/supervisor-model.md §5). Nil or an error degrades to the
// built-in defaults.
func WithHumanFactors(fn func(ctx context.Context) (observation.HumanFactors, error)) Option {
	return func(s *Server) { s.humanFactors = fn }
}

// humanFactorsFor resolves the coefficients with the documented fallback:
// a missing hook or a failed read never breaks the dashboard.
func (s *Server) humanFactorsFor(ctx context.Context) observation.HumanFactors {
	if s.humanFactors == nil {
		return observation.DefaultHumanFactors
	}
	factors, err := s.humanFactors(ctx)
	if err != nil {
		s.log.Warn("load human factors; using defaults", "error", err)
		return observation.DefaultHumanFactors
	}
	return factors
}

// applyAreaAliases rewrites the topic/entity lists through the alias map:
// hidden values are dropped, aliased values collapse into their canonical
// name. Duplicate entries after the mapping are removed; order of survivors
// is preserved.
func applyAreaAliases(items []observation.Knowledge, aliases map[string]AreaAlias) []observation.Knowledge {
	if len(aliases) == 0 || items == nil {
		return items
	}
	remap := func(values []string) []string {
		if len(values) == 0 {
			return nil
		}
		result := make([]string, 0, len(values))
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			alias, ok := aliases[strings.ToLower(strings.TrimSpace(value))]
			if ok && alias.Hidden {
				continue
			}
			if ok && alias.Canonical != "" {
				value = alias.Canonical
			}
			trimmed := strings.TrimSpace(value)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			result = append(result, trimmed)
		}
		if len(result) == 0 {
			return nil
		}
		return result
	}
	for i := range items {
		items[i].Topics = remap(items[i].Topics)
		items[i].Entities = remap(items[i].Entities)
	}
	return items
}

// NewServer builds the journal API server with access to its non-handler
// surfaces (the debug counters). New is the handler-only convenience form.
func NewServer(store observation.Store, log *slog.Logger, gate *controlplane.Gate, opts ...Option) *Server {
	s := &Server{store: store, gate: gate, log: log, now: time.Now, knowledgeCache: map[string]knowledgeCacheEntry{}}
	for _, opt := range opts {
		opt(s)
	}
	counters := newEndpointCounters()
	s.counters = counters
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/observations/events", s.appendObservations)
	mux.HandleFunc("GET /v1/observations/events", s.listObservations)
	mux.HandleFunc("GET /v1/observations/events/{id...}", s.getObservationEvent)
	mux.HandleFunc("GET /v1/observations/stats/health", s.observationHealthStats)
	mux.HandleFunc("GET /v1/observations/knowledge", s.listObservationKnowledge)
	mux.HandleFunc("GET /v1/observations/knowledge/diff", s.diffKnowledge)
	mux.HandleFunc("GET /v1/observations/knowledge/chain", s.knowledgeChain)
	mux.HandleFunc("GET /v1/observations/runs/state", s.runState)
	mux.HandleFunc("GET /v1/observations/runs/compare", s.compareRuns)
	mux.HandleFunc("POST /v1/observations/knowledge/invalidate", s.invalidateObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/knowledge/challenge", s.challengeObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/knowledge/promote", s.promoteObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/knowledge/explore", s.exploreObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/hints", s.activateObservationHints)
	mux.HandleFunc("PUT /v1/observations/knowledge/embeddings", s.putKnowledgeEmbeddings)
	mux.HandleFunc("GET /v1/observations/knowledge/embeddings/missing", s.getKnowledgeEmbeddingGaps)
	mux.HandleFunc("GET /v1/observations/knowledge/embeddings/neighbors", s.getKnowledgeEmbeddingNeighbors)
	mux.HandleFunc("GET /v1/observations/hints/effectiveness", s.getHintEffectiveness)
	s.handler = logRequests(log, s.gate.Authenticate(counters.gateMiddleware(counters.middleware(mux))))
	return s
}

// ServeHTTP delegates to the middleware-wrapped mux (NewServer wires it).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// New serves the journal API. A nil gate disables authentication (local
// development); team deployments pass a gate configured from
// JOURNAL_AUTH_TOKENS.
func New(store observation.Store, log *slog.Logger, gate *controlplane.Gate, opts ...Option) http.Handler {
	return NewServer(store, log, gate, opts...)
}

// DebugCounters serves the per-route request accounting for the journal's
// debug listener (JOURNAL_DEBUG_ADDR). Public so main can wire it next to
// net/http/pprof without exposing either on the authenticated API.
func (s *Server) DebugCounters() http.Handler {
	return http.HandlerFunc(s.counters.writeCounters)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "temporality-journal"})
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		log.Info("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
	})
}

type observationBatch struct {
	Events []observation.Event `json:"events"`
}

type observationIngestResult struct {
	Accepted int                      `json:"accepted"`
	Repeated int                      `json:"repeated"`
	Results  []observationEventResult `json:"results"`
}

type observationEventResult struct {
	SourceID string `json:"source_id"`
	EventID  string `json:"event_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) appendObservations(w http.ResponseWriter, r *http.Request) {
	var input observationBatch
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.Events) == 0 || len(input.Events) > 100 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("events must contain between 1 and 100 items"))
		return
	}
	for i := range input.Events {
		// Second line of defense (docs/secrets-management.md §4.3): recognized
		// secret patterns are rewritten before validation and storage, so a
		// leaked token in a tool output never lands in the journal.
		observation.RedactEvent(&input.Events[i])
		if err := input.Events[i].Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, errors.New("events["+strconv.Itoa(i)+"]: "+err.Error()))
			return
		}
	}
	projects := make([]string, 0, len(input.Events))
	seen := map[string]bool{}
	for _, event := range input.Events {
		if project := event.Context.Project; project != "" && !seen[project] {
			seen[project] = true
			projects = append(projects, project)
		}
	}
	if !s.gate.Allow(w, r, controlplane.RoleWriter, projects...) {
		return
	}
	result := observationIngestResult{Results: make([]observationEventResult, 0, len(input.Events))}
	partialFailure := false
	knowledgeProjects := map[string]bool{}
	for _, event := range input.Events {
		event.ReceivedAt = s.now().UTC()
		inserted, err := s.store.AppendObservation(r.Context(), event)
		if inserted && err == nil && strings.HasPrefix(event.Type, "knowledge.") && event.Context.Project != "" {
			knowledgeProjects[event.Context.Project] = true
		}
		entry := observationEventResult{SourceID: event.Source.ID, EventID: event.EventID}
		switch {
		case err != nil:
			entry.Status = "rejected"
			if errors.Is(err, observation.ErrConflict) {
				entry.Error = observation.ErrConflict.Error()
			} else {
				entry.Error = "storage error"
			}
			partialFailure = true
		case inserted:
			entry.Status = "accepted"
			result.Accepted++
		default:
			entry.Status = "repeated"
			result.Repeated++
		}
		result.Results = append(result.Results, entry)
	}
	status := http.StatusOK
	if partialFailure {
		status = http.StatusMultiStatus
	}
	for project := range knowledgeProjects {
		s.invalidateKnowledge(project)
	}
	// Shared-scope events (proposals born shared, promotions) change what
	// every project sees — a per-project drop would leave stale neighbors.
	for _, event := range input.Events {
		if strings.HasPrefix(event.Type, "knowledge.") && (event.Data["scope_kind"] == "organization" || event.Data["scope_kind"] == "org_unit" || event.Type == "knowledge.promoted") {
			s.invalidateAllKnowledge()
			break
		}
	}
	writeJSON(w, status, result)
}

func (s *Server) listObservations(w http.ResponseWriter, r *http.Request) {
	// The project filter may be empty: the listing is global then, constrained
	// server-side to the principal's visible projects (docs/activity-scoping.md
	// §5) — a scoped token no longer needs to know the project list up front.
	principal, allowed := s.gate.AllowQuery(w, r, controlplane.RoleReader, r.URL.Query().Get("project"))
	if !allowed {
		return
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be between 1 and 500"))
			return
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if _, err := observation.DecodeCursor(cursor); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var since, until, knownAt *time.Time
	for name, target := range map[string]**time.Time{"since": &since, "until": &until, "known_at": &knownAt} {
		if value := r.URL.Query().Get(name); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New(name+" must be RFC3339"))
				return
			}
			*target = &parsed
		}
	}
	filter := observation.Filter{
		Project: r.URL.Query().Get("project"), SourceID: r.URL.Query().Get("source_id"), EventID: r.URL.Query().Get("event_id"), Run: r.URL.Query().Get("run"),
		Task: r.URL.Query().Get("task"), Actor: r.URL.Query().Get("actor"),
		Type: r.URL.Query().Get("type"), Since: since, Until: until, KnownAt: knownAt, Limit: limit,
	}
	if filter.Project == "" {
		// Global listing: constrain to the principal's visible set. Run-scoped
		// queries pass through the same constraint — the run's own events carry
		// its project, so an any-of filter yields them for a visible project and
		// an empty page for someone else's run (no access widening by run id).
		if projects, all := principal.AllowedProjects(); !all {
			if len(projects) == 0 {
				writeJSON(w, http.StatusOK, map[string]any{"events": []observation.Event{}, "count": 0, "next_cursor": ""})
				return
			}
			filter.Projects = projects
		}
	}
	page, err := s.store.ListObservationPage(r.Context(), filter, cursor)
	if err != nil {
		// The 500 body is what /v1/agent/operations surfaces in its error
		// banner — a bare "could not list" is undiagnosable. Keep the cause in
		// both the server log and the response.
		s.log.Error("list observation events", "error", err, "project", filter.Project, "type", filter.Type)
		writeError(w, http.StatusInternalServerError, fmt.Errorf("could not list observation events: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": page.Events, "count": len(page.Events), "next_cursor": page.NextCursor})
}

// healthStatsSource is the optional store capability behind the dashboard
// aggregation: stores that can load a whole window in one pass implement it,
// the rest answer 501 rather than degrade to a single listing page.
type healthStatsSource interface {
	HealthEvents(ctx context.Context, since time.Time, projects []string) ([]observation.Event, error)
}

// observationHealthStats serves the system-dashboard health aggregation
// (docs/system-dashboard.md §3.1): one window, aggregated server-side.
func (s *Server) observationHealthStats(w http.ResponseWriter, r *http.Request) {
	// Same auth and scoping as the event listing: a concrete project is checked
	// by the gate, an empty one aggregates the principal's visible projects
	// (docs/activity-scoping.md §5).
	principal, allowed := s.gate.AllowQuery(w, r, controlplane.RoleReader, r.URL.Query().Get("project"))
	if !allowed {
		return
	}
	// The aggregation needs a whole window in one pass; stores that cannot do
	// that say so instead of silently degrading to the first listing page.
	source, ok := s.store.(healthStatsSource)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("health stats not supported by this store"))
		return
	}
	now := s.now()
	since := now.Add(-time.Hour)
	if value := r.URL.Query().Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("since must be RFC3339"))
			return
		}
		// Longer windows would scan the journal beyond any dashboard need; the
		// 31-day cap keeps the unbounded single-pass query bounded.
		if now.Sub(parsed) > 31*24*time.Hour {
			writeError(w, http.StatusBadRequest, errors.New("since must be within the last 31 days"))
			return
		}
		since = parsed
	}
	// until bounds the window's end (supervisor insights compare the current
	// hour against the previous one); events after it are dropped server-side
	// so the stores' window queries stay unchanged.
	until := now
	if value := r.URL.Query().Get("until"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("until must be RFC3339"))
			return
		}
		if parsed.Before(since) {
			writeError(w, http.StatusBadRequest, errors.New("until must not precede since"))
			return
		}
		until = parsed
	}
	var projects []string
	if project := r.URL.Query().Get("project"); project != "" {
		projects = []string{project}
	} else if visible, all := principal.AllowedProjects(); !all {
		if len(visible) == 0 {
			// Nothing visible: zeroed counters with the window fields, not an
			// error — the dashboard renders an empty but well-formed state.
			writeJSON(w, http.StatusOK, observation.ComputeHealthStats(nil, since, now, s.humanFactorsFor(r.Context())))
			return
		}
		projects = visible
	}
	events, err := source.HealthEvents(r.Context(), since, projects)
	if err != nil {
		s.log.Error("health stats window query", "error", err, "project", r.URL.Query().Get("project"))
		writeError(w, http.StatusInternalServerError, fmt.Errorf("could not compute health stats: %w", err))
		return
	}
	if !until.Equal(now) {
		kept := events[:0]
		for _, event := range events {
			if !event.OccurredAt.After(until) {
				kept = append(kept, event)
			}
		}
		events = kept
	}
	writeJSON(w, http.StatusOK, observation.ComputeHealthStats(events, since, until, s.humanFactorsFor(r.Context())))
}

func (s *Server) getObservationEvent(w http.ResponseWriter, r *http.Request) {
	// Direct evidence lookup for the knowledge dossier (docs/human-knowledge-
	// explorer.md §8): refs like r/event/000001 carry only the event id, so the
	// resolution goes through the event_id filter instead of the (source, id)
	// primary key. The optional source_id query param disambiguates when two
	// producers reuse an id.
	eventID := strings.TrimSpace(r.PathValue("id"))
	if eventID == "" {
		writeError(w, http.StatusBadRequest, errors.New("event id is required"))
		return
	}
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	principal, allowed := s.gate.AllowQuery(w, r, controlplane.RoleReader, project)
	if !allowed {
		return
	}
	filter := observation.Filter{
		Project: project, SourceID: strings.TrimSpace(r.URL.Query().Get("source_id")),
		EventID: eventID, Limit: 2,
	}
	if filter.Project == "" {
		// Same visibility rule as the listing: a global lookup stays inside the
		// principal's visible projects.
		if projects, all := principal.AllowedProjects(); !all {
			if len(projects) == 0 {
				writeError(w, http.StatusNotFound, observation.ErrNotFound)
				return
			}
			filter.Projects = projects
		}
	}
	page, err := s.store.ListObservationPage(r.Context(), filter, "")
	if err != nil {
		s.log.Error("get observation event", "error", err, "event_id", eventID)
		writeError(w, http.StatusInternalServerError, fmt.Errorf("could not load observation event: %w", err))
		return
	}
	if len(page.Events) == 0 {
		writeError(w, http.StatusNotFound, observation.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": page.Events[0]})
}

type invalidateObservationRequest struct {
	KnowledgeID   string                 `json:"knowledge_id"`
	Project       string                 `json:"project"`
	Run           string                 `json:"run,omitempty"`
	Actor         observation.Actor      `json:"actor"`
	Reason        string                 `json:"reason"`
	ReplacementID string                 `json:"replacement_id,omitempty"`
	Evidence      []observation.Evidence `json:"evidence,omitempty"`
}

func (s *Server) projectKnowledge(r *http.Request, project string, asOf, knownAt *time.Time) ([]observation.Event, error) {
	return s.loadEvents(r.Context(), observation.Filter{Project: project, Until: asOf, KnownAt: knownAt, Limit: 500})
}

// loadEvents pages a single filter to exhaustion.
func (s *Server) loadEvents(ctx context.Context, filter observation.Filter) ([]observation.Event, error) {
	filter.Limit = 500
	var result []observation.Event
	cursor := ""
	for {
		page, err := s.store.ListObservationPage(ctx, filter, cursor)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Events...)
		if page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
}

// visibleKnowledgeEvents loads everything the knowledge projection for a
// project needs: the project's own event stream, plus shared knowledge —
// events whose data carries an org_unit/organization scope (proposed directly
// or via knowledge.promoted) together with the full cross-project lifecycle
// of those knowledge items (docs/knowledge-evolution.md §5–6). Post-projection,
// knowledgeVisibleTo still filters by effective scope.
func (s *Server) visibleKnowledgeEvents(ctx context.Context, project string, asOf, knownAt *time.Time) ([]observation.Event, []string, error) {
	projectEvents, err := s.loadEvents(ctx, observation.Filter{Project: project, Until: asOf, KnownAt: knownAt})
	if err != nil {
		return nil, nil, err
	}
	var chain []string
	if s.projectUnits != nil {
		chain, err = s.projectUnits(ctx, project)
		if err != nil {
			s.log.Warn("resolve project org units; shared org-unit knowledge hidden", "project", project, "error", err)
			chain = nil
		}
	}
	// Shared-scope markers: knowledge.proposed born shared plus every
	// knowledge.promoted event (they restate the target scope in data).
	globalShared, err := s.loadEvents(ctx, observation.Filter{ScopeKind: "organization", Until: asOf, KnownAt: knownAt})
	if err != nil {
		return nil, nil, err
	}
	merged := mergeObservationEvents(projectEvents, globalShared)
	if len(chain) > 0 {
		unitShared, err := s.loadEvents(ctx, observation.Filter{ScopeKind: "org_unit", ScopeIDs: chain, Until: asOf, KnownAt: knownAt})
		if err != nil {
			return nil, nil, err
		}
		merged = mergeObservationEvents(merged, unitShared)
	}
	// Pull the complete lifecycle (lifecycle + hint telemetry from any
	// project) for knowledge discovered through the shared path, so state and
	// reuse stats stay correct outside the origin project.
	sharedIDs := make(map[string]bool)
	for _, event := range merged {
		if event.Type == "knowledge.proposed" || event.Type == "knowledge.promoted" {
			if id := stringData(event.Data, "knowledge_id"); id != "" && event.Context.Project != project {
				sharedIDs[id] = true
			}
		}
	}
	if len(sharedIDs) > 0 {
		ids := make([]string, 0, len(sharedIDs))
		for id := range sharedIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		related, err := s.loadEvents(ctx, observation.Filter{KnowledgeIDs: ids, Until: asOf, KnownAt: knownAt})
		if err != nil {
			return nil, nil, err
		}
		merged = mergeObservationEvents(merged, related)
	}
	return merged, chain, nil
}

func mergeObservationEvents(groups ...[]observation.Event) []observation.Event {
	var total int
	for _, group := range groups {
		total += len(group)
	}
	merged := make([]observation.Event, 0, total)
	seen := make(map[string]bool, total)
	for _, group := range groups {
		for _, event := range group {
			key := event.Source.ID + "\x00" + event.EventID
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, event)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		if !merged[i].OccurredAt.Equal(merged[j].OccurredAt) {
			return merged[i].OccurredAt.Before(merged[j].OccurredAt)
		}
		if !merged[i].ReceivedAt.Equal(merged[j].ReceivedAt) {
			return merged[i].ReceivedAt.Before(merged[j].ReceivedAt)
		}
		if merged[i].Source.ID != merged[j].Source.ID {
			return merged[i].Source.ID < merged[j].Source.ID
		}
		return merged[i].EventID < merged[j].EventID
	})
	return merged
}

// withAreaAliases projects the knowledge topics/entities through the
// judge-normalized alias map. A missing hook or a read error keeps the raw
// values — normalization must never break the listing.
func (s *Server) withAreaAliases(ctx context.Context, project string, knowledge []observation.Knowledge) []observation.Knowledge {
	if s.topicAliases == nil || len(knowledge) == 0 {
		return knowledge
	}
	aliases, err := s.topicAliases(ctx, project)
	if err != nil {
		s.log.Warn("load area aliases; areas stay raw", "project", project, "error", err)
		return knowledge
	}
	return applyAreaAliases(knowledge, aliases)
}

// knowledgeVisibleTo decides whether a projected knowledge item is visible to
// a project: its own items always are, organization scope is global, and an
// org_unit scope requires the unit to be in the project's visibility chain.
func knowledgeVisibleTo(item observation.Knowledge, project string, unitChain []string) bool {
	if item.Project == project {
		return true
	}
	switch item.ScopeKind {
	case "organization":
		return true
	case "org_unit":
		for _, unit := range unitChain {
			if unit == item.ScopeID {
				return true
			}
		}
	}
	return false
}

func stringData(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

// knowledgeCacheTTL bounds how long a projected knowledge listing stays
// warm. The projection walks every knowledge event of the project (plus
// shared-scope events) per request — the supervisor's conflict panel and the
// explorer pay it on every call. A short TTL turns the N-lookup fallback walk
// and page flips into single projections; any knowledge-mutating call drops
// the project's entry immediately, so staleness is bounded by TTL anyway.
const knowledgeCacheTTL = 15 * time.Second

type knowledgeCacheEntry struct {
	items    []observation.Knowledge
	chain    []string
	computed time.Time
}

// cachedKnowledge returns the project's visible, alias-normalized knowledge —
// the projection the listing (and every filter/pagination on top of it)
// builds on. Bitemporal queries skip the cache: their inputs are the
// timestamps themselves. The returned slice is shared between requests —
// callers must not sort or filter it in place.
func (s *Server) cachedKnowledge(ctx context.Context, project string, asOf, knownAt *time.Time) ([]observation.Knowledge, []string, error) {
	if asOf != nil || knownAt != nil {
		return s.computeKnowledge(ctx, project, asOf, knownAt)
	}
	s.knowledgeMu.Lock()
	entry, ok := s.knowledgeCache[project]
	s.knowledgeMu.Unlock()
	if ok && time.Since(entry.computed) < knowledgeCacheTTL {
		return entry.items, entry.chain, nil
	}
	items, chain, err := s.computeKnowledge(ctx, project, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	s.knowledgeMu.Lock()
	// Cap the cache: an installation with hundreds of projects must not grow
	// it unbounded; a full map drop on overflow is fine (next reads recompute).
	if len(s.knowledgeCache) >= 64 && !ok {
		s.knowledgeCache = map[string]knowledgeCacheEntry{}
	}
	s.knowledgeCache[project] = knowledgeCacheEntry{items: items, chain: chain, computed: time.Now()}
	s.knowledgeMu.Unlock()
	return items, chain, nil
}

// invalidateKnowledge drops the project's cached projection. Called from the
// knowledge-mutating handlers (ingest of knowledge.* events, invalidate,
// challenge, promote) so a manual action is visible without waiting the TTL.
func (s *Server) invalidateKnowledge(project string) {
	s.knowledgeMu.Lock()
	delete(s.knowledgeCache, project)
	s.knowledgeMu.Unlock()
}

// invalidateAllKnowledge drops every project's projection: scope changes
// (promotion to org_unit/organization, shared-scope proposals) change what
// OTHER projects see, so a single-project drop would leave stale listings
// everywhere else.
func (s *Server) invalidateAllKnowledge() {
	s.knowledgeMu.Lock()
	s.knowledgeCache = map[string]knowledgeCacheEntry{}
	s.knowledgeMu.Unlock()
}

func (s *Server) computeKnowledge(ctx context.Context, project string, asOf, knownAt *time.Time) ([]observation.Knowledge, []string, error) {
	events, chain, err := s.visibleKnowledgeEvents(ctx, project, asOf, knownAt)
	if err != nil {
		return nil, nil, err
	}
	knowledge, err := observation.ProjectKnowledge(events)
	if err != nil {
		return nil, nil, err
	}
	knowledge = filterKnowledgeVisible(knowledge, project, chain)
	knowledge = s.withAreaAliases(ctx, project, knowledge)
	return knowledge, chain, nil
}

func (s *Server) listObservationKnowledge(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeError(w, http.StatusBadRequest, errors.New("project is required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleReader, project) {
		return
	}
	var asOf, knownAt *time.Time
	for name, target := range map[string]**time.Time{"as_of": &asOf, "known_at": &knownAt} {
		if value := r.URL.Query().Get(name); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New(name+" must be RFC3339"))
				return
			}
			*target = &parsed
		}
	}
	query, err := parseKnowledgeListQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	knowledge, _, err := s.cachedKnowledge(r.Context(), project, asOf, knownAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	if knowledgeID := r.URL.Query().Get("knowledge_id"); knowledgeID != "" {
		// NOT knowledge[:0]: filtering in place would corrupt the shared
		// cached slice — an append into the zeroed head overwrites entries
		// other readers still hold.
		filtered := make([]observation.Knowledge, 0, len(knowledge))
		for _, item := range knowledge {
			if item.ID == knowledgeID {
				filtered = append(filtered, item)
			}
		}
		if len(filtered) == 0 {
			writeError(w, http.StatusNotFound, observation.ErrKnowledgeNotFound)
			return
		}
		knowledge = filtered
	}
	// Deterministic order, then post-projection filters (state/scope/text are
	// projection outputs, so they cannot live in SQL) and cursor pagination.
	// Without a limit the full filtered set is returned — the legacy callers
	// (run inspector, kernel skill memory) rely on that shape.
	sortKnowledgeList(knowledge)
	facets := knowledgeFacetsFor(knowledge, query)
	filtered := filterKnowledgeList(knowledge, query)
	page, nextCursor := paginateKnowledgeList(filtered, query.Cursor, query.Limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"knowledge": page, "count": len(page), "total": len(filtered),
		"next_cursor": nextCursor, "facets": facets,
	})
}

func (s *Server) diffKnowledge(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeError(w, http.StatusBadRequest, errors.New("project is required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleReader, project) {
		return
	}
	var from, to *time.Time
	for name, target := range map[string]**time.Time{"from": &from, "to": &to} {
		value := r.URL.Query().Get(name)
		if value == "" {
			writeError(w, http.StatusBadRequest, errors.New(name+" is required"))
			return
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New(name+" must be RFC3339"))
			return
		}
		*target = &parsed
	}
	if !from.Before(*to) {
		writeError(w, http.StatusBadRequest, errors.New("from must be before to"))
		return
	}
	eventsFrom, chain, err := s.visibleKnowledgeEvents(r.Context(), project, from, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load knowledge at from"))
		return
	}
	eventsTo, _, err := s.visibleKnowledgeEvents(r.Context(), project, to, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load knowledge at to"))
		return
	}
	knowledgeFrom, err := observation.ProjectKnowledge(eventsFrom)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	knowledgeTo, err := observation.ProjectKnowledge(eventsTo)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	knowledgeFrom = filterKnowledgeVisible(knowledgeFrom, project, chain)
	knowledgeTo = filterKnowledgeVisible(knowledgeTo, project, chain)
	fromByID := make(map[string]*observation.Knowledge, len(knowledgeFrom))
	for i := range knowledgeFrom {
		fromByID[knowledgeFrom[i].ID] = &knowledgeFrom[i]
	}
	toByID := make(map[string]*observation.Knowledge, len(knowledgeTo))
	for i := range knowledgeTo {
		toByID[knowledgeTo[i].ID] = &knowledgeTo[i]
	}
	var added, removed, changed []observation.Knowledge
	for id, toItem := range toByID {
		if fromItem, existed := fromByID[id]; !existed {
			added = append(added, *toItem)
		} else if fromItem.State != toItem.State || fromItem.Proposition != toItem.Proposition {
			changed = append(changed, *toItem)
		}
	}
	for id, fromItem := range fromByID {
		if _, exists := toByID[id]; !exists {
			removed = append(removed, *fromItem)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": project, "from": from, "to": to,
		"added": added, "removed": removed, "changed": changed,
		"count": map[string]int{"added": len(added), "removed": len(removed), "changed": len(changed)},
	})
}

type ChainEntry struct {
	Type    string         `json:"type"` // appeared, recalled, injected, decided, acted, outcome
	EventID string         `json:"event_id"`
	At      time.Time      `json:"at"`
	Details map[string]any `json:"details,omitempty"`
}

func (s *Server) knowledgeChain(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	knowledgeID := strings.TrimSpace(r.URL.Query().Get("knowledge_id"))
	if project == "" || knowledgeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("project and knowledge_id are required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleReader, project) {
		return
	}
	// Shared knowledge chains cross projects: use the merged visible loader so
	// promoted knowledge shows its full lifecycle regardless of origin project.
	allEvents, chainUnits, err := s.visibleKnowledgeEvents(r.Context(), project, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load events"))
		return
	}
	// The chain must not leak knowledge the project cannot see: verify the
	// target projects into the visible set before walking its events.
	knowledgeAll, err := observation.ProjectKnowledge(allEvents)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	visible := false
	for _, item := range knowledgeAll {
		if item.ID == knowledgeID && knowledgeVisibleTo(item, project, chainUnits) {
			visible = true
			break
		}
	}
	if !visible {
		writeError(w, http.StatusNotFound, observation.ErrKnowledgeNotFound)
		return
	}
	var chain []ChainEntry
	// Index hint_offered events by hint_id for linking.
	hintByID := map[string]observation.Event{}
	for _, ev := range allEvents {
		if ev.Type == "hint.offered" {
			hintID, _ := ev.Data["hint_id"].(string)
			if hintID != "" {
				hintByID[hintID] = ev
			}
		}
	}
	for _, ev := range allEvents {
		switch ev.Type {
		case "knowledge.proposed":
			if kid, _ := ev.Data["knowledge_id"].(string); kid == knowledgeID {
				chain = append(chain, ChainEntry{Type: "appeared", EventID: ev.EventID, At: ev.OccurredAt, Details: map[string]any{"proposition": ev.Data["proposition"]}})
			}
		case "hint.offered":
			if kid, _ := ev.Data["knowledge_id"].(string); kid == knowledgeID {
				chain = append(chain, ChainEntry{Type: "recalled", EventID: ev.EventID, At: ev.OccurredAt, Details: map[string]any{"hint_id": ev.Data["hint_id"], "matched_by": ev.Data["matched_by"]}})
			}
		case "hint.used":
			if kid, _ := ev.Data["knowledge_id"].(string); kid == knowledgeID {
				chain = append(chain, ChainEntry{Type: "injected", EventID: ev.EventID, At: ev.OccurredAt, Details: map[string]any{"hint_id": ev.Data["hint_id"]}})
			}
		case "hint.outcome":
			if kid, _ := ev.Data["knowledge_id"].(string); kid == knowledgeID {
				chain = append(chain, ChainEntry{Type: "outcome", EventID: ev.EventID, At: ev.OccurredAt, Details: map[string]any{"outcome": ev.Data["outcome"], "hint_id": ev.Data["hint_id"]}})
			}
		case "knowledge.confirmed", "knowledge.invalidated", "knowledge.superseded", "knowledge.corrected", "knowledge.promoted":
			if kid, _ := ev.Data["knowledge_id"].(string); kid == knowledgeID {
				details := map[string]any{"event_type": ev.Type, "reason": ev.Data["reason"]}
				if ev.Type == "knowledge.promoted" {
					details["scope_kind"] = ev.Data["scope_kind"]
					details["scope_id"] = ev.Data["scope_id"]
				}
				if replacement, _ := ev.Data["replacement_id"].(string); replacement != "" {
					details["replacement_id"] = replacement
				}
				chain = append(chain, ChainEntry{Type: "lifecycle", EventID: ev.EventID, At: ev.OccurredAt, Details: details})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge_id": knowledgeID, "project": project, "chain": chain, "count": len(chain)})
}

type RunStateEntry struct {
	Type    string         `json:"type"` // run.started, turn.started, model.completed, tool.started, tool.completed, tool.failed, turn.completed, run.completed
	EventID string         `json:"event_id"`
	At      time.Time      `json:"at"`
	Details map[string]any `json:"details,omitempty"`
}

func (s *Server) runState(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	runID := strings.TrimSpace(r.URL.Query().Get("run"))
	if project == "" || runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("project and run are required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleReader, project) {
		return
	}
	var at *time.Time
	if value := r.URL.Query().Get("at"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("at must be RFC3339"))
			return
		}
		at = &parsed
	}
	filter := observation.Filter{Project: project, Run: runID, Until: at, Limit: 500}
	var allEvents []observation.Event
	cursor := ""
	for {
		page, err := s.store.ListObservationPage(r.Context(), filter, cursor)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("could not load events"))
			return
		}
		allEvents = append(allEvents, page.Events...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	var entries []RunStateEntry
	for _, ev := range allEvents {
		switch ev.Type {
		case "run.started", "turn.started", "model.completed", "tool.started", "tool.completed", "tool.failed", "turn.completed", "run.completed":
			entries = append(entries, RunStateEntry{Type: ev.Type, EventID: ev.EventID, At: ev.OccurredAt, Details: ev.Data})
		}
	}
	status := "unknown"
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		if last.Type == "run.completed" {
			status = "completed"
		} else if last.Type == "run.started" || last.Type == "turn.started" || last.Type == "model.completed" || last.Type == "tool.started" || last.Type == "tool.completed" || last.Type == "turn.completed" {
			status = "in_progress"
		}
	}
	// Per-run human factors (docs/supervisor-model.md §7): what this session
	// cost its humans — measured wait (HWT) and estimated attention (HAB).
	human := observation.DefaultHumanFactors
	if s.humanFactors != nil {
		if configured, err := s.humanFactors(r.Context()); err == nil {
			human = configured
		}
	}
	factors := observation.ComputeRunHumanFactors(allEvents, human)
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "run": runID, "at": at, "status": status, "events": entries, "count": len(entries), "human": factors})
}

type RunSummary struct {
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	Turns         int    `json:"turns"`
	ToolCalls     int    `json:"tool_calls"`
	MCPRequests   int    `json:"mcp_requests"`
	KnowledgeUsed int    `json:"knowledge_used"`
}

func (s *Server) compareRuns(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	run1 := strings.TrimSpace(r.URL.Query().Get("run1"))
	run2 := strings.TrimSpace(r.URL.Query().Get("run2"))
	if project == "" || run1 == "" || run2 == "" {
		writeError(w, http.StatusBadRequest, errors.New("project, run1, and run2 are required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleReader, project) {
		return
	}
	summarize := func(runID string) (RunSummary, error) {
		filter := observation.Filter{Project: project, Run: runID, Limit: 500}
		var allEvents []observation.Event
		cursor := ""
		for {
			page, err := s.store.ListObservationPage(r.Context(), filter, cursor)
			if err != nil {
				return RunSummary{}, err
			}
			allEvents = append(allEvents, page.Events...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		var summary RunSummary
		summary.RunID = runID
		for _, ev := range allEvents {
			switch ev.Type {
			case "run.started":
				summary.Status = "started"
			case "run.completed":
				summary.Status = "completed"
			case "turn.completed":
				summary.Turns++
			case "tool.started":
				summary.ToolCalls++
				if strings.HasPrefix(ev.Data["tool"].(string), "mcp__") {
					summary.MCPRequests++
				}
			case "hint.used":
				summary.KnowledgeUsed++
			}
		}
		return summary, nil
	}
	s1, err := summarize(run1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load run1 events"))
		return
	}
	s2, err := summarize(run2)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load run2 events"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "run1": s1, "run2": s2})
}

func (s *Server) invalidateObservationKnowledge(w http.ResponseWriter, r *http.Request) {
	var input invalidateObservationRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.KnowledgeID = strings.TrimSpace(input.KnowledgeID)
	input.Project = strings.TrimSpace(input.Project)
	input.Actor.ID = strings.TrimSpace(input.Actor.ID)
	input.Reason = strings.TrimSpace(input.Reason)
	input.ReplacementID = strings.TrimSpace(input.ReplacementID)
	// Reason is optional (docs/knowledge-evolution.md: a human who sees an
	// outdated rule should not have to write an essay) — the journal invariant
	// (knowledge.invalidated carries a reason) is kept with a neutral default.
	if input.Reason == "" {
		input.Reason = "outdated (human verdict, no comment)"
	}
	if input.KnowledgeID == "" || input.Project == "" || input.Actor.ID == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("knowledge_id, project, and actor.id are required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleOperator, input.Project) {
		return
	}
	projectEvents, chain, err := s.visibleKnowledgeEvents(r.Context(), input.Project, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(projectEvents)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	knowledge = filterKnowledgeVisible(knowledge, input.Project, chain)
	var target *observation.Knowledge
	for i := range knowledge {
		if knowledge[i].ID == input.KnowledgeID {
			target = &knowledge[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, observation.ErrKnowledgeNotFound)
		return
	}
	if target.State == "corrected" || target.State == "superseded" || target.State == "invalidated" {
		writeError(w, http.StatusConflict, errors.New("knowledge is already retired"))
		return
	}
	// Invalidation with a successor is a supersession, not a bare retirement:
	// hints then offer the replacement in place of the retired rule
	// (docs/plan-priming-relevance.md §10, «замена предлагается вместо
	// исключения»). The successor must be a visible, live item — never itself,
	// never another retired rule.
	eventType := "knowledge.invalidated"
	eventData := map[string]any{"knowledge_id": input.KnowledgeID, "reason": input.Reason, "method": "manual"}
	if input.ReplacementID != "" {
		if input.ReplacementID == input.KnowledgeID {
			writeError(w, http.StatusUnprocessableEntity, errors.New("replacement_id must differ from knowledge_id"))
			return
		}
		var replacement *observation.Knowledge
		for i := range knowledge {
			if knowledge[i].ID == input.ReplacementID {
				replacement = &knowledge[i]
				break
			}
		}
		if replacement == nil {
			writeError(w, http.StatusNotFound, errors.New("replacement knowledge not found"))
			return
		}
		if replacement.State == "corrected" || replacement.State == "superseded" || replacement.State == "invalidated" {
			writeError(w, http.StatusConflict, errors.New("replacement knowledge is already retired"))
			return
		}
		eventType = "knowledge.superseded"
		eventData["replacement_id"] = input.ReplacementID
	}
	event := observation.Event{
		Schema: observation.Schema, EventID: newUUID(), OccurredAt: s.now().UTC(),
		Source:   observation.Source{ID: "temporality-manual", Integration: "temporality", Version: "1"},
		Context:  observation.Context{Project: input.Project, Run: input.Run, Actor: input.Actor},
		Type:     eventType,
		Data:     eventData,
		Evidence: input.Evidence,
	}
	if err = event.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	event.ReceivedAt = s.now().UTC()
	if _, err = s.store.AppendObservation(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not record invalidation"))
		return
	}
	s.invalidateKnowledge(input.Project)
	writeJSON(w, http.StatusCreated, event)
}

type promoteObservationRequest struct {
	KnowledgeID string                 `json:"knowledge_id"`
	Project     string                 `json:"project"`
	Run         string                 `json:"run,omitempty"`
	Actor       observation.Actor      `json:"actor"`
	ScopeKind   string                 `json:"scope_kind"`
	ScopeID     string                 `json:"scope_id,omitempty"`
	Reason      string                 `json:"reason"`
	Evidence    []observation.Evidence `json:"evidence,omitempty"`
}

type challengeObservationRequest struct {
	KnowledgeID string                 `json:"knowledge_id"`
	Project     string                 `json:"project"`
	Run         string                 `json:"run,omitempty"`
	Actor       observation.Actor      `json:"actor"`
	Reason      string                 `json:"reason"`
	Evidence    []observation.Evidence `json:"evidence,omitempty"`
}

// challengeObservationKnowledge records a manual doubt: the knowledge stays
// visible but is marked challenged, so hints offer it with a caution instead
// of presenting it as an established rule (docs/knowledge-evolution.md §4).
// Unlike invalidation it never retires the item — a later confirmation or
// correction still decides its fate.
func (s *Server) challengeObservationKnowledge(w http.ResponseWriter, r *http.Request) {
	var input challengeObservationRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.KnowledgeID = strings.TrimSpace(input.KnowledgeID)
	input.Project = strings.TrimSpace(input.Project)
	input.Actor.ID = strings.TrimSpace(input.Actor.ID)
	input.Reason = strings.TrimSpace(input.Reason)
	// Reason is optional — a doubt is a flag, not a verdict; the journal
	// invariant (knowledge.challenged carries a reason) is kept with a
	// neutral default.
	if input.Reason == "" {
		input.Reason = "needs verification (human flag, no comment)"
	}
	if input.KnowledgeID == "" || input.Project == "" || input.Actor.ID == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("knowledge_id, project, and actor.id are required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleOperator, input.Project) {
		return
	}
	events, chain, err := s.visibleKnowledgeEvents(r.Context(), input.Project, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(events)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	knowledge = filterKnowledgeVisible(knowledge, input.Project, chain)
	var target *observation.Knowledge
	for i := range knowledge {
		if knowledge[i].ID == input.KnowledgeID {
			target = &knowledge[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, observation.ErrKnowledgeNotFound)
		return
	}
	if target.State == "corrected" || target.State == "superseded" || target.State == "invalidated" {
		writeError(w, http.StatusConflict, errors.New("knowledge is already retired"))
		return
	}
	event := observation.Event{
		Schema: observation.Schema, EventID: newUUID(), OccurredAt: s.now().UTC(),
		Source:   observation.Source{ID: "temporality-manual", Integration: "temporality", Version: "1"},
		Context:  observation.Context{Project: input.Project, Run: input.Run, Actor: input.Actor},
		Type:     "knowledge.challenged",
		Data:     map[string]any{"knowledge_id": input.KnowledgeID, "reason": input.Reason, "method": "manual"},
		Evidence: input.Evidence,
	}
	if err = event.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	event.ReceivedAt = s.now().UTC()
	if _, err = s.store.AppendObservation(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not record challenge"))
		return
	}
	s.invalidateKnowledge(input.Project)
	writeJSON(w, http.StatusCreated, event)
}

// filterKnowledgeVisible narrows a projected knowledge list to what the
// project may see: own items, organization-scoped items, and org_unit-scoped
// items whose unit is in the project's visibility chain.
func filterKnowledgeVisible(knowledge []observation.Knowledge, project string, unitChain []string) []observation.Knowledge {
	filtered := make([]observation.Knowledge, 0, len(knowledge))
	for _, item := range knowledge {
		if knowledgeVisibleTo(item, project, unitChain) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// promoteObservationKnowledge records an explicit scope widening
// (project → org_unit / organization) with provenance: the actor, the reason
// and the previous scope all land in the emitted event
// (docs/knowledge-evolution.md §5: generalization is a separate deliberate
// act, never an automatic consequence of looking useful).
func (s *Server) promoteObservationKnowledge(w http.ResponseWriter, r *http.Request) {
	var input promoteObservationRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.KnowledgeID = strings.TrimSpace(input.KnowledgeID)
	input.Project = strings.TrimSpace(input.Project)
	input.ScopeKind = strings.TrimSpace(input.ScopeKind)
	input.ScopeID = strings.TrimSpace(input.ScopeID)
	input.Actor.ID = strings.TrimSpace(input.Actor.ID)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		input.Reason = "promoted by human (no comment)"
	}
	if input.KnowledgeID == "" || input.Project == "" || input.Actor.ID == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("knowledge_id, project, and actor.id are required"))
		return
	}
	if input.ScopeKind != "org_unit" && input.ScopeKind != "organization" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("scope_kind must be org_unit or organization"))
		return
	}
	if input.ScopeKind == "org_unit" && input.ScopeID == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("scope_id is required for org_unit scope"))
		return
	}
	if input.ScopeKind == "organization" && input.ScopeID != "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("scope_id must be empty for organization scope"))
		return
	}
	// Widening visibility affects other projects: match the invalidate gate.
	if !s.gate.Allow(w, r, controlplane.RoleOperator, input.Project) {
		return
	}
	if s.unitExists != nil && input.ScopeKind == "org_unit" && !s.unitExists(r.Context(), input.ScopeID) {
		writeError(w, http.StatusUnprocessableEntity, errors.New("scope_id refers to an unknown org unit"))
		return
	}
	events, chain, err := s.visibleKnowledgeEvents(r.Context(), input.Project, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(events)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	knowledge = filterKnowledgeVisible(knowledge, input.Project, chain)
	var target *observation.Knowledge
	for i := range knowledge {
		if knowledge[i].ID == input.KnowledgeID {
			target = &knowledge[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, observation.ErrKnowledgeNotFound)
		return
	}
	if target.State == "corrected" || target.State == "superseded" || target.State == "invalidated" {
		writeError(w, http.StatusConflict, errors.New("retired knowledge cannot be promoted"))
		return
	}
	if target.ScopeKind == "organization" {
		writeError(w, http.StatusConflict, errors.New("knowledge is already organization-scoped"))
		return
	}
	if target.ScopeKind == input.ScopeKind && target.ScopeID == input.ScopeID {
		writeError(w, http.StatusConflict, errors.New("knowledge already has the requested scope"))
		return
	}
	event := observation.Event{
		Schema: observation.Schema, EventID: newUUID(), OccurredAt: s.now().UTC(),
		Source:  observation.Source{ID: "temporality-manual", Integration: "temporality", Version: "1"},
		Context: observation.Context{Project: input.Project, Run: input.Run, Actor: input.Actor},
		Type:    "knowledge.promoted",
		Data: map[string]any{
			"knowledge_id": input.KnowledgeID, "scope_kind": input.ScopeKind, "scope_id": input.ScopeID,
			"from_scope_kind": target.ScopeKind, "from_scope_id": target.ScopeID,
			"reason": input.Reason,
		},
		Evidence: input.Evidence,
	}
	if err = event.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	event.ReceivedAt = s.now().UTC()
	if _, err = s.store.AppendObservation(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not record promotion"))
		return
	}
	s.invalidateAllKnowledge()
	writeJSON(w, http.StatusCreated, event)
}

type knowledgeExploreObservationRequest struct {
	Project           string   `json:"project"`
	Operation         string   `json:"operation"`
	Question          string   `json:"question,omitempty"`
	Targets           []string `json:"targets,omitempty"`
	Relations         []string `json:"relations,omitempty"`
	MaxDepth          int      `json:"max_depth,omitempty"`
	MaxItems          int      `json:"max_items,omitempty"`
	IncludeCandidates bool     `json:"include_candidates,omitempty"`
	AsOf              string   `json:"as_of,omitempty"`
	KnownAt           string   `json:"known_at,omitempty"`
}

// exploreObservationKnowledge backs the read-only research surface
// (docs/agent-knowledge-exploration.md §4): orient, search, expand, trace,
// compare and synthesize over the caller's visible knowledge. Visibility,
// bitemporal slicing and authorization resolve exactly like every other
// knowledge endpoint — the engine only ever sees what the caller may see.
func (s *Server) exploreObservationKnowledge(w http.ResponseWriter, r *http.Request) {
	var input knowledgeExploreObservationRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.Project = strings.TrimSpace(input.Project)
	input.Operation = strings.TrimSpace(input.Operation)
	input.Question = strings.TrimSpace(input.Question)
	if input.Project == "" {
		writeError(w, http.StatusBadRequest, errors.New("project is required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleReader, input.Project) {
		return
	}
	var asOf, knownAt *time.Time
	if input.AsOf != "" {
		parsed, err := time.Parse(time.RFC3339Nano, input.AsOf)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("as_of must be RFC3339"))
			return
		}
		asOf = &parsed
	}
	if input.KnownAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, input.KnownAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("known_at must be RFC3339"))
			return
		}
		knownAt = &parsed
	}
	switch input.Operation {
	case observation.ExploreOrient:
		// orient without a question is the full area map ranked by size — that
		// is the landing view of the knowledge explorer, not an input error.
	case observation.ExploreSearch:
		if input.Question == "" {
			writeError(w, http.StatusUnprocessableEntity, errors.New(input.Operation+" requires a question"))
			return
		}
	case observation.ExploreExpand, observation.ExploreTrace:
		if len(input.Targets) == 0 {
			writeError(w, http.StatusUnprocessableEntity, errors.New(input.Operation+" requires targets"))
			return
		}
	case observation.ExploreCompare:
		// compare without targets auto-detects conflicts across the visible
		// base — that is its discovery mode, not an input error.
	case observation.ExploreSynthesize:
		if len(input.Targets) == 0 && input.Question == "" {
			writeError(w, http.StatusUnprocessableEntity, errors.New(input.Operation+" requires targets or a question"))
			return
		}
	default:
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("operation must be one of %s, %s, %s, %s, %s, %s", observation.ExploreOrient, observation.ExploreSearch, observation.ExploreExpand, observation.ExploreTrace, observation.ExploreCompare, observation.ExploreSynthesize))
		return
	}
	if len([]rune(input.Question)) > 4096 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("question exceeds the 4096 character limit"))
		return
	}
	if len(input.Targets) > observation.ExploreMaxTargets {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("targets must contain at most %d ids", observation.ExploreMaxTargets))
		return
	}
	if len(input.Relations) > observation.ExploreMaxRelations {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("relations must contain at most %d types", observation.ExploreMaxRelations))
		return
	}
	if input.MaxItems < 0 || input.MaxItems > observation.ExploreMaxItems {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("max_items must be between 1 and %d", observation.ExploreMaxItems))
		return
	}
	if input.MaxDepth < 0 || input.MaxDepth > observation.ExploreMaxDepth {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("max_depth must be between 1 and %d", observation.ExploreMaxDepth))
		return
	}
	events, chain, err := s.visibleKnowledgeEvents(r.Context(), input.Project, asOf, knownAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(events)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	knowledge = filterKnowledgeVisible(knowledge, input.Project, chain)
	knowledge = s.withAreaAliases(r.Context(), input.Project, knowledge)
	result := observation.ExploreKnowledge(knowledge, observation.ExploreQuery{
		Operation: input.Operation, Question: input.Question, Targets: input.Targets,
		Relations: input.Relations, MaxDepth: input.MaxDepth, MaxItems: input.MaxItems,
		IncludeCandidates: input.IncludeCandidates,
	})
	writeJSON(w, http.StatusOK, result)
}

type observationHintRequest struct {
	Project    string            `json:"project"`
	Run        string            `json:"run,omitempty"`
	Task       string            `json:"task,omitempty"`
	Actor      observation.Actor `json:"actor,omitempty"`
	Query      string            `json:"query,omitempty"`
	Tool       string            `json:"tool,omitempty"`
	ToolResult string            `json:"tool_result,omitempty"`
	Entities   []string          `json:"entities,omitempty"`
	Topics     []string          `json:"topics,omitempty"`
	Limit      int               `json:"limit,omitempty"`
	// ActiveSkills carries the digests of skills already attached to the
	// agent's prompt (plan-priming-relevance.md §7); duplicating candidates
	// are demoted, not excluded.
	ActiveSkills []observation.HintSkill `json:"active_skills,omitempty"`
	// IssuedKnowledgeIDs suppresses knowledge already offered in this run.
	IssuedKnowledgeIDs []string `json:"issued_knowledge_ids,omitempty"`
	// Embedding is the semantic vector of the query text, computed by the
	// kernel with the supervisor embeddings model. Optional: without it the
	// matcher runs purely lexical.
	Embedding []float32 `json:"embedding,omitempty"`
	// EmbeddingModel names the model that produced the query embedding; the
	// stored knowledge vectors stamped with the same model are the match set.
	EmbeddingModel string `json:"embedding_model,omitempty"`
	// TeamRunRoot names the team program root run (docs/agent-teams.md §8):
	// knowledge proposed or confirmed by any run under that root (the root
	// itself or ids prefixed "<root>/") is additionally admitted to the hints
	// of every slot of the team.
	TeamRunRoot string `json:"team_root,omitempty"`
}

func (s *Server) activateObservationHints(w http.ResponseWriter, r *http.Request) {
	var input observationHintRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.Project = strings.TrimSpace(input.Project)
	input.Query = strings.TrimSpace(input.Query)
	input.Tool = strings.TrimSpace(input.Tool)
	if len([]rune(input.Query)) > 4096 || len([]rune(input.ToolResult)) > 8192 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("query exceeds the 4096 character limit or tool_result exceeds the 8192 character limit"))
		return
	}
	queryText := strings.TrimSpace(strings.Join([]string{input.Query, input.Tool, input.ToolResult}, " "))
	if input.Project == "" || (queryText == "" && len(input.Entities) == 0 && len(input.Topics) == 0) {
		writeError(w, http.StatusUnprocessableEntity, errors.New("project and at least one query/entity/topic are required"))
		return
	}
	if !s.gate.Allow(w, r, controlplane.RoleReader, input.Project) {
		return
	}
	if input.Limit < 0 || input.Limit > 20 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("limit must be between 1 and 20"))
		return
	}
	if len(input.ActiveSkills) > 16 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("active_skills must contain at most 16 skills"))
		return
	}
	skillSummaryRunes := 0
	for _, skill := range input.ActiveSkills {
		skillSummaryRunes += len([]rune(skill.Summary))
	}
	if skillSummaryRunes > 8192 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("active_skills summaries exceed the 8192 character limit in total"))
		return
	}
	if len(input.IssuedKnowledgeIDs) > 64 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("issued_knowledge_ids must contain at most 64 ids"))
		return
	}
	if len([]rune(input.TeamRunRoot)) > 256 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("team_root exceeds the 256 character limit"))
		return
	}
	events, chain, err := s.visibleKnowledgeEvents(r.Context(), input.Project, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(events)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	knowledge = filterKnowledgeVisible(knowledge, input.Project, chain)
	// Team-run scoping (docs/agent-teams.md §8): knowledge whose proposal or
	// confirmation originated under the team root is collected from the same
	// event stream the projection just consumed — no extra store round-trip.
	var teamRunKnowledgeIDs []string
	if input.TeamRunRoot != "" {
		root := input.TeamRunRoot
		seenTeamRun := make(map[string]bool)
		for _, event := range events {
			if event.Type != "knowledge.proposed" && event.Type != "knowledge.confirmed" {
				continue
			}
			if run := event.Context.Run; run == root || strings.HasPrefix(run, root+"/") {
				if id, _ := event.Data["knowledge_id"].(string); id != "" && !seenTeamRun[id] {
					seenTeamRun[id] = true
					teamRunKnowledgeIDs = append(teamRunKnowledgeIDs, id)
				}
			}
		}
	}
	// Semantic tier inputs: the query embedding rides the request; knowledge
	// vectors come from the store when it supports them (postgres journal).
	// Any failure degrades to the lexical matcher.
	var knowledgeEmbeddings map[string][]float32
	embeddingUsed := false
	if len(input.Embedding) > 0 && input.EmbeddingModel != "" {
		if len(input.Embedding) <= 4096 {
			if store, ok := s.store.(interface {
				ListKnowledgeEmbeddings(context.Context, string) (map[string][]float32, error)
			}); ok {
				if vectors, err := store.ListKnowledgeEmbeddings(r.Context(), input.EmbeddingModel); err != nil {
					s.log.Warn("load knowledge embeddings; semantic tier skipped", "error", err)
				} else if len(vectors) > 0 {
					knowledgeEmbeddings = vectors
					embeddingUsed = true
				}
			}
		}
	}
	hints := observation.FindHints(knowledge, observation.HintQuery{Text: queryText, Entities: input.Entities, Topics: input.Topics, Limit: input.Limit, ActiveSkills: input.ActiveSkills, IssuedKnowledgeIDs: input.IssuedKnowledgeIDs, TeamRunKnowledgeIDs: teamRunKnowledgeIDs, Embedding: input.Embedding, KnowledgeEmbeddings: knowledgeEmbeddings})
	activationID := newUUID()
	context := observation.Context{Project: input.Project, Run: input.Run, Task: input.Task, Actor: input.Actor}
	activationEvent := observation.Event{
		Schema: observation.Schema, EventID: activationID, OccurredAt: s.now().UTC(),
		Source:  observation.Source{ID: "temporality-activation", Integration: "temporality", Version: "1"},
		Context: context, Type: "hint.query",
		Data: map[string]any{"activation_id": activationID, "candidate_count": len(hints), "matcher": observation.HintsAlgorithmVersion, "algorithm_version": observation.HintsAlgorithmVersion, "has_text_query": queryText != "", "active_skill_count": len(input.ActiveSkills), "issued_knowledge_count": len(input.IssuedKnowledgeIDs), "team_root": input.TeamRunRoot, "team_run_knowledge_count": len(teamRunKnowledgeIDs), "semantic_tier": embeddingUsed},
	}
	if err = appendObservationNow(r, s.store, s.now, activationEvent); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not record hint activation"))
		return
	}
	for i := range hints {
		offerID := newUUID()
		hints[i].HintID = offerID
		hints[i].OfferEventID = offerID
		offered := observation.Event{
			Schema: observation.Schema, EventID: offerID, OccurredAt: s.now().UTC(),
			Source:  observation.Source{ID: "temporality-activation", Integration: "temporality", Version: "1"},
			Context: context, Type: "hint.offered",
			// The proposition rides along so run timelines show what was actually
			// suggested — raw hint/knowledge ids mean nothing to a human.
			Data: map[string]any{"hint_id": offerID, "activation_id": activationID, "knowledge_id": hints[i].KnowledgeID, "state": hints[i].State, "matched_by": hints[i].MatchedBy, "proposition": hints[i].Proposition, "algorithm_version": observation.HintsAlgorithmVersion},
		}
		if err = appendObservationNow(r, s.store, s.now, offered); err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("could not record offered hint"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"activation_id": activationID, "matcher": observation.HintsAlgorithmVersion, "algorithm_version": observation.HintsAlgorithmVersion,
		"context_block": map[string]any{"type": "temporality.memory", "activation_id": activationID, "items": hints},
		"hints":         hints, "count": len(hints),
	})
}

func appendObservationNow(r *http.Request, store observation.Store, now func() time.Time, event observation.Event) error {
	event.ReceivedAt = now().UTC()
	if err := event.Validate(); err != nil {
		return err
	}
	_, err := store.AppendObservation(r.Context(), event)
	return err
}

func decodeJSON(r *http.Request, target any) error {
	return decodeJSONLimit(r, target, 1<<20)
}

// decodeJSONLimit is decodeJSON with an explicit body cap. Large-payload
// endpoints (embedding batches) use a bigger one: the default 1 MiB cuts a
// realistic vector batch mid-value and json answers "unexpected EOF".
func decodeJSONLimit(r *http.Request, target any, limit int64) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, limit))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}
