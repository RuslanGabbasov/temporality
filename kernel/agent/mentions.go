package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Chat mentions (docs/chat-mentions.md): a task prompt may reference workspace
// entities three ways — `@artifact:otchet-2026` (the composer's canonical
// form), `@otchet-2026` (bare at-mention), or the plain pasted id `otchet-2026`
// (the artifact page's click-to-copy handle, dropped straight into Telegram or
// Matrix). The run start resolves every reference against the workspace
// registry and appends one compact system block, so the model works with exact
// ids and titles regardless of channel. Bodies never ride the block — content
// still arrives through the agent's own chunked tools (docs/artifact-registry.md §6).
const mentionMaxEntries = 12

var mentionKinds = []string{"artifact", "skill", "knowledge", "saga", "task", "agent", "team"}

var (
	mentionExplicitRe = regexp.MustCompile(`(?i)@(artifact|skill|knowledge|saga|task|agent|team):([A-Za-z0-9][A-Za-z0-9._-]*)`)
	mentionBareAtRe   = regexp.MustCompile(`@([A-Za-z0-9][A-Za-z0-9._-]*)`)
	// Plain pasted ids are slugs: the embedded dash keeps ordinary prose words
	// ("update", "отчёт") out of the candidate set. Non-matching tokens simply
	// fail to resolve against the registry index and cost nothing.
	mentionPlainIDRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._-]*-[A-Za-z0-9._-]+`)
)

// MentionRef is one candidate reference found in a prompt.
type MentionRef struct {
	// Kind names the registry ("artifact", …); empty means "any kind".
	Kind string
	ID   string
	// Explicit marks the `@…` forms: only those earn a "not found" note — a
	// dash-token from prose that happens to match nothing stays silent.
	Explicit bool
}

// ParseMentions extracts candidate references in reading order, deduplicated
// per (kind, id). Explicit spans mask the weaker patterns, so `@artifact:x`
// is one reference, not three.
func ParseMentions(text string) []MentionRef {
	type span struct{ start, end int }
	var refs []MentionRef
	var masked []span
	seen := map[string]bool{}
	add := func(ref MentionRef, start, end int) {
		key := ref.Kind + "\x00" + ref.ID
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, ref)
		masked = append(masked, span{start, end})
	}
	covered := func(start, end int) bool {
		for _, s := range masked {
			if start < s.end && end > s.start {
				return true
			}
		}
		return false
	}
	for _, m := range mentionExplicitRe.FindAllStringSubmatchIndex(text, -1) {
		add(MentionRef{Kind: strings.ToLower(text[m[2]:m[3]]), ID: text[m[4]:m[5]], Explicit: true}, m[0], m[1])
	}
	for _, m := range mentionBareAtRe.FindAllStringSubmatchIndex(text, -1) {
		id := text[m[2]:m[3]]
		if isMentionKindName(id) {
			// A dropped scaffold (`@artifact:` with no id yet) names nothing;
			// mask it so the plain pass cannot resurrect its parts.
			masked = append(masked, span{m[0], m[1]})
			continue
		}
		if covered(m[0], m[1]) {
			continue
		}
		// An `@` glued to a word is an e-mail/local-part, not a mention — and
		// the whole word stays out of the plain-id pass as well.
		if m[0] > 0 && isMentionWordByte(text[m[0]-1]) {
			masked = append(masked, span{m[0], m[1]})
			continue
		}
		add(MentionRef{ID: id, Explicit: true}, m[0], m[1])
	}
	for _, m := range mentionPlainIDRe.FindAllStringIndex(text, -1) {
		if covered(m[0], m[1]) {
			continue
		}
		add(MentionRef{ID: text[m[0]:m[1]]}, m[0], m[1])
	}
	return refs
}

func isMentionWordByte(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9')
}

func isMentionKindName(id string) bool {
	for _, kind := range mentionKinds {
		if id == kind {
			return true
		}
	}
	return false
}

// MentionRequest resolves the references of one run prompt.
type MentionRequest struct {
	Project string
	Prompt  string
}

// mentionEntity is one resolved reference: enough identity for the model to
// act (exact id + title + lifecycle detail), never a body.
type mentionEntity struct {
	Kind   string
	ID     string
	Title  string
	Detail string
}

// ResolveMentions renders the referenced-entity block of the run prompt. Best
// effort by design: on registry failure it returns "" and the run proceeds as
// if mentions did not exist — the agent still resolves ids with its tools, and
// a lookup outage never costs the user their task.
func (a *Activities) ResolveMentions(ctx context.Context, request MentionRequest) (string, error) {
	refs := ParseMentions(request.Prompt)
	if len(refs) == 0 {
		return "", nil
	}
	need := map[string]bool{}
	for _, ref := range refs {
		if ref.Kind != "" {
			need[ref.Kind] = true
			continue
		}
		for _, kind := range mentionKinds {
			need[kind] = true
		}
	}
	indexes := map[string]map[string]mentionEntity{}
	for _, kind := range mentionKinds {
		if !need[kind] {
			continue
		}
		if index := a.mentionIndex(ctx, request.Project, kind); len(index) > 0 {
			indexes[kind] = index
		}
	}
	var resolved []mentionEntity
	var unresolved []string
	seen := map[string]bool{}
	for _, ref := range refs {
		hits := 0
		for _, kind := range mentionKinds {
			if ref.Kind != "" && ref.Kind != kind {
				continue
			}
			entity, ok := indexes[kind][ref.ID]
			if !ok {
				continue
			}
			hits++
			key := kind + "\x00" + ref.ID
			if !seen[key] && len(resolved) < mentionMaxEntries {
				seen[key] = true
				resolved = append(resolved, entity)
			}
		}
		if hits == 0 && ref.Explicit {
			name := ref.ID
			if ref.Kind != "" {
				name = ref.Kind + ":" + ref.ID
			}
			unresolved = append(unresolved, "@"+name)
		}
	}
	if len(resolved) == 0 && len(unresolved) == 0 {
		return "", nil
	}
	if len(unresolved) > mentionMaxEntries {
		unresolved = append(unresolved[:mentionMaxEntries], "…")
	}
	return renderMentionBlock(resolved, unresolved), nil
}

// mentionIndex fetches the metadata registry of one kind. Every source is
// capped and metadata-only; a missing source degrades that kind to "unknown",
// never to an error.
func (a *Activities) mentionIndex(ctx context.Context, project, kind string) map[string]mentionEntity {
	index := map[string]mentionEntity{}
	switch kind {
	case "artifact":
		body, status, err := a.workspaceDo(ctx, http.MethodGet, a.WorkspaceURL+"/v1/workspace/artifacts?limit=500", nil)
		if err != nil || status >= 400 {
			return nil
		}
		var page struct {
			Artifacts []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Status  string `json:"status"`
				Version int    `json:"version"`
			} `json:"artifacts"`
		}
		if json.Unmarshal([]byte(body), &page) != nil {
			return nil
		}
		for _, item := range page.Artifacts {
			detail := ""
			if item.Status != "" {
				detail = "status: " + item.Status
			}
			if item.Version > 1 {
				detail += fmt.Sprintf(", version: %d", item.Version)
			}
			index[item.ID] = mentionEntity{Kind: kind, ID: item.ID, Title: item.Title, Detail: strings.TrimPrefix(detail, ", ")}
		}
	case "skill", "team", "agent":
		endpoint := a.WorkspaceURL + "/v1/workspace/" + kind + "s"
		body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
		if err != nil || status >= 400 {
			return nil
		}
		// The list endpoints key their payload by plural kind ("skills", …).
		var keyed map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &keyed) != nil {
			return nil
		}
		raw, ok := keyed[kind+"s"]
		if !ok {
			return nil
		}
		var items []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"`
		}
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for _, item := range items {
			detail := ""
			if item.Enabled != nil && !*item.Enabled {
				detail = "disabled"
			}
			index[item.ID] = mentionEntity{Kind: kind, ID: item.ID, Title: item.Name, Detail: detail}
		}
	case "task":
		endpoint := a.WorkspaceURL + "/v1/workspace/projects/" + url.PathEscape(project) + "/tasks"
		body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
		if err != nil || status >= 400 {
			return nil
		}
		var page struct {
			Tasks []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"tasks"`
		}
		if json.Unmarshal([]byte(body), &page) != nil {
			return nil
		}
		for _, item := range page.Tasks {
			index[item.ID] = mentionEntity{Kind: kind, ID: item.ID, Title: item.Title}
		}
	case "saga":
		// Sagas are event-sourced (docs/sagas.md): the live projection over the
		// journal's saga.* events is the cheap index — the cost rollup endpoint
		// rescans the whole project and has no business running per run start.
		events, err := a.fetchSagaEvents(ctx, project, "")
		if err != nil {
			return nil
		}
		for _, saga := range liveSagas(events) {
			detail := ""
			if saga.Goal != "" {
				detail = "goal: " + truncateRunes(saga.Goal, 100)
			}
			index[saga.SagaID] = mentionEntity{Kind: kind, ID: saga.SagaID, Title: saga.Title, Detail: detail}
		}
	case "knowledge":
		endpoint := a.TemporalityURL + "/v1/observations/knowledge?project=" + url.QueryEscape(project) + "&limit=500"
		body, status, err := a.journalDo(ctx, http.MethodGet, endpoint)
		if err != nil || status >= 400 {
			return nil
		}
		var page struct {
			Knowledge []struct {
				ID          string `json:"id"`
				Proposition string `json:"proposition"`
				State       string `json:"state"`
			} `json:"knowledge"`
		}
		if json.Unmarshal([]byte(body), &page) != nil {
			return nil
		}
		for _, item := range page.Knowledge {
			index[item.ID] = mentionEntity{Kind: kind, ID: item.ID, Title: truncateRunes(item.Proposition, 120), Detail: "state: " + item.State}
		}
	}
	return index
}

// journalDo is the authed journal GET (the hints/knowledge pattern); kept
// separate from workspaceDo because the journal and the workspace API take
// different tokens.
func (a *Activities) journalDo(ctx context.Context, method, endpoint string) (string, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return "", 0, err
	}
	if token := a.apiToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.HTTP.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	// The 1 MiB guard mirrors workspaceDo: a registry page that does not fit
	// is a lookup miss, not a crash.
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return string(body), response.StatusCode, nil
}

func renderMentionBlock(entities []mentionEntity, unresolved []string) string {
	var b strings.Builder
	b.WriteString("[Referenced workspace entities]\n")
	b.WriteString("The user message references these workspace entities by id (an @mention or a pasted id). The ids are exact — use them as given. Bodies never ride this block: fetch content with the matching tools when the task needs it (artifact_read for artifacts).\n")
	for _, entity := range entities {
		title := truncateRunes(entity.Title, 120)
		if title == "" {
			title = entity.ID
		}
		line := fmt.Sprintf("- %s %q (id: %s)", entity.Kind, title, entity.ID)
		if entity.Detail != "" {
			line += " — " + entity.Detail
		}
		b.WriteString(line + "\n")
	}
	if len(unresolved) > 0 {
		b.WriteString("No workspace entity matches " + strings.Join(unresolved, ", ") + " — do not invent one; ask the user for the right id if the task needs it.\n")
	}
	return b.String()
}
