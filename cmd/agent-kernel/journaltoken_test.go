package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// journalProbeServer answers the anonymous probe and (optionally) the
// authenticated probe with fixed statuses; authStatus 0 means «accept any
// bearer token».
func journalProbeServer(t *testing.T, anonStatus int, authStatus int) *httptest.Server {
	t.Helper()
	if authStatus == 0 {
		authStatus = http.StatusOK
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(anonStatus)
			return
		}
		w.WriteHeader(authStatus)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestResolveJournalTokenOpenJournalStaysAnonymous(t *testing.T) {
	server := journalProbeServer(t, http.StatusOK, 0)
	ensureCalled := false
	token, source, err := resolveJournalToken(context.Background(), server.Client(), server.URL, "", func() (string, error) {
		ensureCalled = true
		return "minted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "" || source != journalTokenAnonymous {
		t.Fatalf("token/source = %q/%q, want empty/anonymous", token, source)
	}
	if ensureCalled {
		t.Fatal("an open journal must never be provisioned: a service user row would switch auth on")
	}
}

func TestResolveJournalTokenEnvTokenAccepted(t *testing.T) {
	server := journalProbeServer(t, http.StatusUnauthorized, http.StatusOK)
	ensureCalled := false
	token, source, err := resolveJournalToken(context.Background(), server.Client(), server.URL, "env-token", func() (string, error) {
		ensureCalled = true
		return "minted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "env-token" || source != journalTokenEnv {
		t.Fatalf("token/source = %q/%q, want env-token/env", token, source)
	}
	if ensureCalled {
		t.Fatal("a working env token must not trigger provisioning")
	}
}

func TestResolveJournalTokenEmptyEnvProvisions(t *testing.T) {
	server := journalProbeServer(t, http.StatusUnauthorized, 0)
	token, source, err := resolveJournalToken(context.Background(), server.Client(), server.URL, "", func() (string, error) {
		return "minted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "minted" || source != journalTokenService {
		t.Fatalf("token/source = %q/%q, want minted/service", token, source)
	}
}

func TestResolveJournalTokenRejectedEnvFallsBackToAcceptedService(t *testing.T) {
	// The env token is refused, the provisioned service principal passes.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "":
			w.WriteHeader(http.StatusUnauthorized)
		case "Bearer minted":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)
	token, source, err := resolveJournalToken(context.Background(), server.Client(), server.URL, "bad-env-token", func() (string, error) {
		return "minted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "minted" || source != journalTokenService {
		t.Fatalf("token/source = %q/%q, want minted/service", token, source)
	}
}

func TestResolveJournalTokenRejectedServiceIsReported(t *testing.T) {
	// A journal that refuses even the provisioned principal (boot race: it
	// never learned the token) yields the service-rejected source instead of
	// silently converging on a token that 401s forever.
	server := journalProbeServer(t, http.StatusUnauthorized, http.StatusUnauthorized)
	token, source, err := resolveJournalToken(context.Background(), server.Client(), server.URL, "bad-env-token", func() (string, error) {
		return "minted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "minted" || source != journalTokenServiceRejected {
		t.Fatalf("token/source = %q/%q, want minted/service-rejected", token, source)
	}
}

func TestResolveJournalTokenUnreachableKeepsEnvAndNeverProvisions(t *testing.T) {
	ensureCalled := false
	token, source, err := resolveJournalToken(context.Background(), &http.Client{}, "http://127.0.0.1:1", "env-token", func() (string, error) {
		ensureCalled = true
		return "minted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "env-token" || source != journalTokenEnvStale {
		t.Fatalf("token/source = %q/%q, want env-token/env-unverified", token, source)
	}
	if ensureCalled {
		t.Fatal("an unreachable journal says nothing about auth; provisioning would be a guess")
	}
}

func TestResolveJournalTokenServerErrorIsNotAnAuthVerdict(t *testing.T) {
	server := journalProbeServer(t, http.StatusInternalServerError, 0)
	ensureCalled := false
	_, source, err := resolveJournalToken(context.Background(), server.Client(), server.URL, "env-token", func() (string, error) {
		ensureCalled = true
		return "minted", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if source != journalTokenEnvStale {
		t.Fatalf("source = %q, want env-unverified", source)
	}
	if ensureCalled {
		t.Fatal("a 5xx probe must not provision")
	}
}

func TestResolveJournalTokenProvisioningErrorPropagates(t *testing.T) {
	server := journalProbeServer(t, http.StatusUnauthorized, 0)
	_, _, err := resolveJournalToken(context.Background(), server.Client(), server.URL, "", func() (string, error) {
		return "", errors.New("deactivated")
	})
	if err == nil || err.Error() != "deactivated" {
		t.Fatalf("err = %v, want provisioning error", err)
	}
}
