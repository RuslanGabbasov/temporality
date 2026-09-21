package opsenv

import (
	"strings"
	"testing"
)

func TestBillingProdAuthFlips(t *testing.T) {
	v3 := World{Session: 1, FlipAt: 6}
	if got := v3.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "pat"}).Status; got != 403 {
		t.Fatalf("prod v3 pat = %d, want 403", got)
	}
	if got := v3.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "oauth"}).Status; got != 200 {
		t.Fatalf("prod v3 oauth = %d, want 200", got)
	}
	v5 := World{Session: 6, FlipAt: 6}
	if got := v5.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "oauth"}).Status; got != 403 {
		t.Fatalf("prod v5 oauth = %d, want 403", got)
	}
	if got := v5.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "pat"}).Status; got != 200 {
		t.Fatalf("prod v5 pat = %d, want 200", got)
	}
}

func TestBillingStagingIsStableAndDifferent(t *testing.T) {
	for _, session := range []int{1, 6, 10} {
		w := World{Session: session, FlipAt: 6}
		if got := w.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "pat", Environment: EnvStaging}).Status; got != 403 {
			t.Fatalf("session %d staging pat = %d, want 403", session, got)
		}
		result := w.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "oauth", Environment: EnvStaging})
		if result.Status != 200 || !strings.Contains(result.Body, "INV-5002") {
			t.Fatalf("session %d staging oauth = %d %s", session, result.Status, result.Body)
		}
	}
}

func TestBillingStagingAndProdDataDiffer(t *testing.T) {
	w := World{Session: 1}
	prod := w.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "oauth"})
	staging := w.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "oauth", Environment: EnvStaging})
	if prod.Body == staging.Body {
		t.Fatalf("staging must serve different data: %s", prod.Body)
	}
}

func TestFailureCauses(t *testing.T) {
	w := World{Session: 1}
	if got := w.Execute(Call{Service: SvcBilling, Resource: "invoices.list", Auth: "pat"}).Cause; got != CauseAuth {
		t.Fatalf("auth rejection cause = %q, want %q", got, CauseAuth)
	}
	if got := w.Execute(Call{Service: SvcNotifications, Resource: "templates.list", Auth: "none"}).Cause; got != CauseParameter {
		t.Fatalf("missing param cause = %q, want %q", got, CauseParameter)
	}
	if got := w.Execute(Call{Service: SvcFlaky, Resource: "reports.get", Auth: "pat"}).Cause; got != CauseServer {
		t.Fatalf("flaky 503 cause = %q, want %q", got, CauseServer)
	}
	if got := w.Execute(Call{Service: SvcBilling, Resource: "invoices.get", Auth: "oauth", Params: map[string]any{"id": "INV-0000", "version": "2"}}).Cause; got != CauseNotFound {
		t.Fatalf("404 cause = %q, want %q", got, CauseNotFound)
	}
}

func TestFlakyGatewayMirrorMode(t *testing.T) {
	w := World{Session: 1}
	direct := w.Execute(Call{Service: SvcFlaky, Resource: "reports.get", Auth: "pat"})
	if direct.Status != 503 {
		t.Fatalf("direct = %d, want 503", direct.Status)
	}
	mirror := w.Execute(Call{Service: SvcFlaky, Resource: "reports.get", Auth: "pat", Params: map[string]any{"mirror": true}})
	if mirror.Status != 200 || !strings.Contains(mirror.Body, "REP-42") {
		t.Fatalf("mirror = %d %s", mirror.Status, mirror.Body)
	}
	status := w.Execute(Call{Service: SvcFlaky, Resource: "status.page", Auth: "pat"})
	if status.Status != 200 || !strings.Contains(status.Body, "mirror") {
		t.Fatalf("status.page = %d %s", status.Status, status.Body)
	}
}

func TestPaymentsRequiresServiceToken(t *testing.T) {
	w := World{Session: 1}
	call := Call{Service: SvcPayments, Resource: "refunds.get", Auth: "service_token", Params: map[string]any{"id": "RFD-9002"}}
	result := w.Execute(call)
	if result.Status != 200 || !strings.Contains(result.Body, "settled") {
		t.Fatalf("service_token call = %d %s", result.Status, result.Body)
	}
	if got := w.Execute(Call{Service: SvcPayments, Resource: "refunds.get", Auth: "oauth", Params: map[string]any{"id": "RFD-9002"}}).Status; got != 403 {
		t.Fatalf("oauth = %d, want 403", got)
	}
}

func TestTemplatesRequiresChannelParam(t *testing.T) {
	w := World{Session: 1}
	if got := w.Execute(Call{Service: SvcNotifications, Resource: "templates.list", Auth: "none"}).Status; got != 400 {
		t.Fatalf("missing channel = %d, want 400", got)
	}
	result := w.Execute(Call{Service: SvcNotifications, Resource: "templates.list", Auth: "none", Params: map[string]any{"channel": "email"}})
	if result.Status != 200 || !strings.Contains(result.Body, "TMPL-77") {
		t.Fatalf("channel=email = %d %s", result.Status, result.Body)
	}
}

func TestSearchPagination(t *testing.T) {
	w := World{Session: 1}
	first := w.Execute(Call{Service: SvcSearch, Resource: "documents.search", Auth: "pat", Params: map[string]any{"query": "quarterly report"}})
	if first.Status != 200 || !strings.Contains(first.Body, "next_cursor") || strings.Contains(first.Body, "DOC-3319") {
		t.Fatalf("page1 = %d %s", first.Status, first.Body)
	}
	third := w.Execute(Call{Service: SvcSearch, Resource: "documents.search", Auth: "pat", Params: map[string]any{"query": "quarterly report", "cursor": "c3"}})
	if third.Status != 200 || !strings.Contains(third.Body, "DOC-3319") {
		t.Fatalf("page3 = %d %s", third.Status, third.Body)
	}
}

func TestInvoiceGetRequiresVersion2(t *testing.T) {
	w := World{Session: 1}
	bad := w.Execute(Call{Service: SvcBilling, Resource: "invoices.get", Auth: "oauth", Params: map[string]any{"id": "INV-2007", "version": "1"}})
	if bad.Status != 400 {
		t.Fatalf("version 1 = %d, want 400", bad.Status)
	}
	good := w.Execute(Call{Service: SvcBilling, Resource: "invoices.get", Auth: "oauth", Params: map[string]any{"id": "INV-2007", "version": "2"}})
	if good.Status != 200 || !strings.Contains(good.Body, "vertex-computing") {
		t.Fatalf("version 2 = %d %s", good.Status, good.Body)
	}
}

func TestMainTasksCoverCoreServices(t *testing.T) {
	seen := map[string]bool{}
	for _, task := range MainTasks() {
		seen[task.Service] = true
		if len(task.Markers) == 0 {
			t.Fatalf("task %s has no markers", task.ID)
		}
	}
	for _, svc := range []string{SvcBilling, SvcPayments, SvcNotifications, SvcSearch} {
		if !seen[svc] {
			t.Fatalf("service %s not covered by main tasks", svc)
		}
	}
}

func TestConflictTasksTargetBothEnvironments(t *testing.T) {
	w := World{Session: 1, FlipAt: 1} // conflict fixture: prod = v5 (pat) from the start
	for _, task := range ConflictTasks() {
		env := task.Environment
		auth := "pat"
		if env == EnvStaging {
			auth = "oauth"
		}
		result := w.Execute(Call{Service: task.Service, Resource: task.Resource, Auth: auth, Environment: env})
		if result.Status != 200 {
			t.Fatalf("task %s solved call = %d %s (auth %s)", task.ID, result.Status, result.Body, auth)
		}
		for _, marker := range task.Markers {
			if !strings.Contains(result.Body, marker) {
				t.Fatalf("task %s marker %s missing from %s", task.ID, marker, result.Body)
			}
		}
	}
}

func TestParseCallDefaultsToProd(t *testing.T) {
	call := ParseCall(map[string]any{"service": SvcBilling, "resource": "invoices.list", "auth": "oauth"})
	if call.Environment != EnvProd {
		t.Fatalf("default environment = %q, want prod", call.Environment)
	}
	staging := ParseCall(map[string]any{"service": SvcBilling, "resource": "invoices.list", "auth": "oauth", "environment": "staging"})
	if staging.Environment != EnvStaging {
		t.Fatalf("staging environment = %q", staging.Environment)
	}
}

func TestDocsDoNotLeakGatewayPolicy(t *testing.T) {
	// Docs may list auth options but must not reveal which one the gateway
	// actually accepts, otherwise there is nothing to remember.
	for _, svc := range AllServices {
		docs := Docs(svc)
		lower := strings.ToLower(docs)
		if strings.Contains(lower, "gateway accepts") || strings.Contains(lower, "only auth") || strings.Contains(lower, "actually") {
			t.Fatalf("docs for %s leak policy: %s", svc, docs)
		}
	}
}
