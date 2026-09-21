// Package opsenv is a deterministic synthetic operations environment for the
// adaptive-memory experiments. It simulates a small fleet of internal HTTP
// services whose public docs claim several auth methods while the gateway
// accepts only a subset per service. The world has two named environments
// (prod/staging); billing-api runs a different API version in each and a
// mid-experiment flip retires the old prod auth method, so previously learned
// knowledge becomes stale. A separate flaky service produces repeated 503s to
// exercise the repeated-failure guardrail.
package opsenv

import (
	"fmt"
	"sort"
	"strings"

	"github.com/temporality-project/temporality/aml/llm"
)

// Tool names.
const (
	ToolAPICall = "api_call"
	ToolAPIDocs = "api_docs"
)

// Services of the synthetic fleet.
const (
	SvcBilling       = "billing-api"
	SvcPayments      = "payments-gw"
	SvcNotifications = "notifications"
	SvcSearch        = "search-index"
	SvcFlaky         = "flaky-gw"
)

// AllServices is the service lexicon shared with the memory layer.
var AllServices = []string{SvcBilling, SvcPayments, SvcNotifications, SvcSearch, SvcFlaky}

// Named deployment environments of the fleet.
const (
	EnvProd    = "prod"
	EnvStaging = "staging"
)

// Failure causes for outcome attribution: a failure with cause "auth" only
// contradicts auth-related memories, "parameter" only contradicts
// param-related memories, anything unattributable leaves memory UNRESOLVED.
const (
	CauseAuth      = "auth"
	CauseParameter = "parameter"
	CauseNotFound  = "not_found"
	CauseServer    = "server"
)

// Task is one measurable unit of work in the environment.
type Task struct {
	ID          string
	Service     string
	Text        string
	Markers     []string // strings that must all appear in the final answer
	Resource    string
	Environment string // prod (default) or staging
}

// MainTasks returns the long-horizon task suite (same as experiment 1, all
// prod, for comparability). Order is fixed across sessions.
func MainTasks() []Task {
	return []Task{
		{
			ID:       "T1",
			Service:  SvcBilling,
			Resource: "invoices.list",
			Text:     "Using the internal API, list the invoices in the billing-api service and report the id and amount of the second invoice.",
			Markers:  []string{"INV-1002", "3400"},
		},
		{
			ID:       "T2",
			Service:  SvcPayments,
			Resource: "refunds.get",
			Text:     "In the payments-gw service, fetch the status of refund RFD-9002 and report its id and status.",
			Markers:  []string{"RFD-9002", "settled"},
		},
		{
			ID:       "T3",
			Service:  SvcNotifications,
			Resource: "templates.list",
			Text:     "The notifications service manages message templates. Report the id of the email template named \"welcome\".",
			Markers:  []string{"TMPL-77"},
		},
		{
			ID:       "T4",
			Service:  SvcSearch,
			Resource: "documents.search",
			Text:     "Search the search-index service for the document titled \"quarterly report\" and report its document id.",
			Markers:  []string{"DOC-3319"},
		},
		{
			ID:       "T5",
			Service:  SvcBilling,
			Resource: "invoices.get",
			Text:     "In billing-api, get the details of invoice INV-2007 and report its vendor.",
			Markers:  []string{"INV-2007", "vertex-computing"},
		},
	}
}

// ConflictTasks returns the environment-conflict suite: the same service and
// resource in two environments that demand different auth methods.
func ConflictTasks() []Task {
	return []Task{
		{
			ID:          "P1",
			Service:     SvcBilling,
			Resource:    "invoices.list",
			Environment: EnvProd,
			Text:        "Using the internal API, list the invoices in the billing-api production environment and report the id and amount of the first invoice.",
			Markers:     []string{"INV-1001", "1200"},
		},
		{
			ID:          "S1",
			Service:     SvcBilling,
			Resource:    "invoices.list",
			Environment: EnvStaging,
			Text:        "In the billing-api staging environment, list the invoices and report the id and amount of the second invoice.",
			Markers:     []string{"INV-5002", "4100"},
		},
	}
}

// GuardrailTasks returns the repeated-failure suite: a service whose primary
// resource returns 503 unless an undocumented parameter is set.
func GuardrailTasks() []Task {
	return []Task{
		{
			ID:       "G1",
			Service:  SvcFlaky,
			Resource: "reports.get",
			Text:     "The flaky-gw service serves the monthly operations report. Fetch it and report the report id and the mode it was served in.",
			Markers:  []string{"REP-42", "mirror"},
		},
	}
}

// Tasks is the default (main) suite; kept for Exp 1 call sites.
func Tasks() []Task { return MainTasks() }

// World executes calls for one session of the experiment. FlipAt > 0 makes
// billing-api prod migrate from v3 (oauth) to v5 (pat) from session FlipAt.
type World struct {
	Session int
	FlipAt  int
}

// New builds a world for the given session (1-based).
func New(session int) *World { return &World{Session: session} }

// FlipActive reports whether the prod auth flip has happened.
func (w World) FlipActive() bool { return w.FlipAt > 0 && w.Session >= w.FlipAt }

// Epoch is 2 once the flip is active (compat label for reports).
func (w World) Epoch() int {
	if w.FlipActive() {
		return 2
	}
	return 1
}

// BillingProdVersion is the current prod API version of billing-api.
func (w World) BillingProdVersion() string {
	if w.FlipActive() {
		return "v5"
	}
	return "v3"
}

// VersionFor returns the API version of a service in an environment
// ("" when the service is not versioned).
func (w World) VersionFor(service, environment string) string {
	if service != SvcBilling {
		return ""
	}
	if environment == EnvStaging {
		return "v4"
	}
	return w.BillingProdVersion()
}

// authStatus reports the gateway status and failure cause for
// (world, service, environment, auth). Docs claim every method is supported;
// the gateway disagrees.
func (w World) authStatus(service, environment, auth string) (int, string) {
	switch service {
	case SvcBilling:
		if environment == EnvStaging {
			// staging runs v4 preview: oauth only.
			if auth == "oauth" {
				return 200, ""
			}
			return 403, CauseAuth
		}
		if w.FlipActive() {
			// prod v5: pat only, oauth retired.
			if auth == "pat" {
				return 200, ""
			}
			return 403, CauseAuth
		}
		// prod v3: oauth only.
		if auth == "oauth" {
			return 200, ""
		}
		return 403, CauseAuth
	case SvcPayments:
		if auth == "service_token" {
			return 200, ""
		}
		if auth == "none" {
			return 401, CauseAuth
		}
		return 403, CauseAuth
	case SvcNotifications:
		return 200, ""
	case SvcSearch:
		if auth == "pat" {
			return 200, ""
		}
		return 401, CauseAuth
	case SvcFlaky:
		if auth == "none" {
			return 401, CauseAuth
		}
		return 200, ""
	default:
		return 404, CauseNotFound
	}
}

type document struct {
	ID    string
	Title string
}

var searchPages = [][]document{
	{{"DOC-1101", "onboarding guide"}, {"DOC-1102", "pricing sheet"}},
	{{"DOC-2205", "release notes"}, {"DOC-2206", "security policy"}},
	{{"DOC-3319", "quarterly report"}, {"DOC-3320", "board minutes"}},
}

const invoicesProd = `{"invoices":[{"id":"INV-1001","amount":1200},{"id":"INV-1002","amount":3400},{"id":"INV-1003","amount":800}]}`

// Staging serves a different data set so environment mix-ups are detectable.
const invoicesStaging = `{"invoices":[{"id":"INV-5001","amount":900},{"id":"INV-5002","amount":4100},{"id":"INV-5003","amount":700}]}`

// Result is a call outcome. Cause is empty on success.
type Result struct {
	Status int
	Body   string
	Cause  string
}

// OK reports gateway-level success.
func (r Result) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Text renders the outcome for the model.
func (r Result) Text() string { return fmt.Sprintf("HTTP %d %s", r.Status, r.Body) }

func errorBody(code int, message, detail string) string {
	body := fmt.Sprintf(`{"error":{"code":%d,"message":%q`, code, message)
	if detail != "" {
		body += fmt.Sprintf(`,"detail":%q`, detail)
	}
	return body + "}}"
}

// Call is a parsed api_call invocation.
type Call struct {
	Service     string
	Resource    string
	Auth        string
	Environment string
	Params      map[string]any
}

// ParseCall extracts the structured call from model tool arguments.
func ParseCall(args map[string]any) Call {
	call := Call{Params: map[string]any{}}
	call.Service, _ = args["service"].(string)
	call.Resource, _ = args["resource"].(string)
	call.Auth, _ = args["auth"].(string)
	call.Environment, _ = args["environment"].(string)
	if call.Environment != EnvStaging {
		call.Environment = EnvProd
	}
	if raw, ok := args["params"].(map[string]any); ok {
		call.Params = raw
	}
	return call
}

// Execute runs one api_call.
func (w World) Execute(call Call) Result {
	switch call.Service {
	case SvcBilling, SvcPayments, SvcNotifications, SvcSearch, SvcFlaky:
	default:
		return Result{Status: 404, Body: errorBody(404, "not_found", "unknown service"), Cause: CauseNotFound}
	}
	if status, cause := w.authStatus(call.Service, call.Environment, call.Auth); status != 200 {
		return Result{Status: status, Body: errorBody(status, statusText(status), ""), Cause: cause}
	}
	switch {
	case call.Service == SvcBilling && call.Resource == "invoices.list":
		if call.Environment == EnvStaging {
			return Result{Status: 200, Body: invoicesStaging}
		}
		return Result{Status: 200, Body: invoicesProd}
	case call.Service == SvcBilling && call.Resource == "invoices.get":
		if id, _ := call.Params["id"].(string); id != "INV-2007" {
			return Result{Status: 404, Body: errorBody(404, "not_found", "no such invoice"), Cause: CauseNotFound}
		}
		if version, _ := call.Params["version"].(string); version != "2" {
			return Result{Status: 400, Body: errorBody(400, "bad_request", "unsupported api version for this resource"), Cause: CauseParameter}
		}
		return Result{Status: 200, Body: `{"id":"INV-2007","vendor":"vertex-computing","amount":9800}`}
	case call.Service == SvcPayments && call.Resource == "refunds.get":
		id, _ := call.Params["id"].(string)
		if id != "RFD-9002" {
			return Result{Status: 404, Body: errorBody(404, "not_found", "no such refund"), Cause: CauseNotFound}
		}
		return Result{Status: 200, Body: `{"id":"RFD-9002","status":"settled","amount":150}`}
	case call.Service == SvcNotifications && call.Resource == "templates.list":
		channel, _ := call.Params["channel"].(string)
		if channel != "email" {
			return Result{Status: 400, Body: errorBody(400, "invalid_request", "channel parameter is required for this operation"), Cause: CauseParameter}
		}
		return Result{Status: 200, Body: `{"templates":[{"id":"TMPL-77","name":"welcome"},{"id":"TMPL-12","name":"password-reset"}]}`}
	case call.Service == SvcSearch && call.Resource == "documents.search":
		if _, ok := call.Params["query"].(string); !ok {
			return Result{Status: 400, Body: errorBody(400, "invalid_request", "query parameter is required"), Cause: CauseParameter}
		}
		cursor, _ := call.Params["cursor"].(string)
		page, next := searchPage(cursor)
		if page == nil {
			return Result{Status: 400, Body: errorBody(400, "invalid_request", "invalid cursor"), Cause: CauseParameter}
		}
		var docs []string
		for _, doc := range page {
			docs = append(docs, fmt.Sprintf(`{"id":%q,"title":%q}`, doc.ID, doc.Title))
		}
		body := `{"documents":[` + strings.Join(docs, ",") + `]`
		if next != "" {
			body += fmt.Sprintf(`,"next_cursor":%q`, next)
		}
		return Result{Status: 200, Body: body + "}"}
	case call.Service == SvcFlaky && call.Resource == "reports.get":
		if mirror, _ := call.Params["mirror"].(bool); !mirror {
			return Result{Status: 503, Body: errorBody(503, "service_unavailable", "reports backend unavailable; see status.page for mitigations"), Cause: CauseServer}
		}
		return Result{Status: 200, Body: `{"report":"REP-42","mode":"mirror"}`}
	case call.Service == SvcFlaky && call.Resource == "status.page":
		return Result{Status: 200, Body: `{"status":"degraded","component":"reports","mitigation":"reports.get is served via mirror mode: set params.mirror=true"}`}
	default:
		return Result{Status: 404, Body: errorBody(404, "not_found", "unknown resource"), Cause: CauseNotFound}
	}
}

func searchPage(cursor string) (page []document, next string) {
	switch cursor {
	case "":
		return searchPages[0], "c2"
	case "c2":
		return searchPages[1], "c3"
	case "c3":
		return searchPages[2], ""
	default:
		return nil, ""
	}
}

func statusText(status int) string {
	switch status {
	case 400:
		return "bad_request"
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 410:
		return "gone"
	case 503:
		return "service_unavailable"
	default:
		return "error"
	}
}

// Docs returns the public documentation for a service. Docs list all
// nominally supported auth methods and required parameters; they do not
// describe gateway policy, which must be discovered by calling.
func Docs(service string) string {
	switch service {
	case SvcBilling:
		return "billing-api public docs:\n" +
			"- environments: prod (stable), staging (preview, api v4)\n" +
			"- invoices.list: params {} ; supported auth: pat, oauth\n" +
			"- invoices.get: params {id:string, version:string} ; supported auth: pat, oauth\n" +
			"NOTE: invoices.get currently serves only api version \"2\"."
	case SvcPayments:
		return "payments-gw public docs:\n" +
			"- refunds.get: params {id:string} ; supported auth: pat, oauth, service_token"
	case SvcNotifications:
		return "notifications public docs:\n" +
			"- templates.list: params {channel:email|sms} ; supported auth: any"
	case SvcSearch:
		return "search-index public docs:\n" +
			"- documents.search: params {query:string, cursor:string optional} ; supported auth: pat, oauth\n" +
			"Results are paginated; follow next_cursor to enumerate more pages."
	case SvcFlaky:
		return "flaky-gw public docs:\n" +
			"- reports.get: params {} ; supported auth: pat, oauth\n" +
			"- status.page: params {} ; supported auth: pat, oauth\n" +
			"NOTE: the reports subsystem has known reliability issues."
	default:
		return "unknown service"
	}
}

// ToolDefs exposes the environment tools in provider schema form.
func ToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name:        ToolAPICall,
			Description: "Call an internal HTTP API. The gateway may reject the request depending on environment, auth method, service policy and parameters.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service":     map[string]any{"type": "string", "enum": AllServices},
					"resource":    map[string]any{"type": "string", "description": "resource path, e.g. invoices.list"},
					"auth":        map[string]any{"type": "string", "enum": []string{"none", "pat", "oauth", "service_token"}, "description": "authentication method"},
					"environment": map[string]any{"type": "string", "enum": []string{EnvProd, EnvStaging}, "description": "deployment environment (default prod)"},
					"params":      map[string]any{"type": "object", "description": "request parameters"},
				},
				"required": []string{"service", "resource", "auth"},
			},
		},
		{
			Name:        ToolAPIDocs,
			Description: "Read the public API documentation of one service.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service": map[string]any{"type": "string", "enum": AllServices},
				},
				"required": []string{"service"},
			},
		},
	}
}

// SortedServices returns the lexicon in stable order.
func SortedServices() []string {
	out := append([]string(nil), AllServices...)
	sort.Strings(out)
	return out
}
