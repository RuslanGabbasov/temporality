package main

import (
	"context"
	"net/http"
	"time"
)

// journal token sources reported by resolveJournalToken.
const (
	journalTokenAnonymous = "anonymous"      // journal is open, no token needed
	journalTokenEnv       = "env"            // configured TEMPORALITY_API_TOKEN works
	journalTokenEnvStale  = "env-unverified" // journal unreachable; keeping the env token as-is
	journalTokenService   = "service"        // auto-provisioned workspace service principal
)

// resolveJournalToken decides which bearer token the kernel uses for its own
// journal calls (event ingest, hints, knowledge, the operations listing).
//
// Fresh installs configure nothing: TEMPORALITY_API_TOKEN is empty and the
// journal's gate switches on only when the first workspace user appears
// (onboarding). From that moment every anonymous kernel call gets 401 — runs
// still execute but publish nothing (invisible in the UI) and
// /v1/agent/operations answers 500 on every poll. Both services share the
// workspace database, so the fix is a service principal the journal picks up
// on its next token reload.
//
// Probing keeps deliberately open dev stands open: a service user row would
// flip auth ON for a tokenless journal, so one is only provisioned after the
// journal explicitly rejected anonymous access.
func resolveJournalToken(ctx context.Context, client *http.Client, journalURL, envToken string, ensure func() (string, error)) (string, string, error) {
	anonStatus, err := probeJournal(ctx, client, journalURL, "")
	if err != nil {
		// Journal unreachable (not up yet, or boot ordering): keep whatever is
		// configured. If the journal turns out to require auth, the operations
		// listing will say so — and a kernel restart after the journal is up
		// resolves the token. Never provision blindly: an open journal must
		// stay open.
		return envToken, journalTokenEnvStale, nil
	}
	if anonStatus == http.StatusOK {
		return "", journalTokenAnonymous, nil
	}
	if anonStatus != http.StatusUnauthorized && anonStatus != http.StatusForbidden {
		// A 5xx (or anything else) is not an auth verdict; keep the env token.
		return envToken, journalTokenEnvStale, nil
	}
	if envToken != "" {
		authStatus, err := probeJournal(ctx, client, journalURL, envToken)
		if err == nil && authStatus == http.StatusOK {
			return envToken, journalTokenEnv, nil
		}
	}
	token, err := ensure()
	if err != nil {
		return "", "", err
	}
	return token, journalTokenService, nil
}

// probeJournal issues one cheap listing request and returns the HTTP status;
// transport failures come back as errors.
func probeJournal(ctx context.Context, client *http.Client, journalURL, token string) (int, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, journalURL+"/v1/observations/events?limit=1", nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}
