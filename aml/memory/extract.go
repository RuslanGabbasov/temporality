package memory

import (
	"fmt"
	"strings"
	"time"
)

// ExtractedExperience is one distilled memory candidate from a finished task.
type ExtractedExperience struct {
	Asset *Asset
	IsNew bool // false when it merges into an existing dedup key
}

// CallRecord is the flattened observation the extractor works on. It is
// exported so world-specific extractors (coding, ...) can build assets from the
// same log the layer records.
type CallRecord struct {
	Seq         int
	Tool        string
	Service     string
	Resource    string
	Auth        string
	Environment string
	Params      map[string]any
	Status      int
	OK          bool
	Cause       string
	Summary     string
}

// ExtractOptions controls extraction.
type ExtractOptions struct {
	SessionID   string
	TaskID      string
	TaskText    string // full task text (coding navigation propositions use it)
	Service     string // task-declared service scope
	Environment string
	Version     string // service API version context ("" when unversioned)
	Success     bool   // only successful tasks yield strategy memories
}

// ExtractExperiences scans the ordered call log for failure→success
// transitions and pagination chains, producing deduplicated assets.
//
// Transition rule (deterministic): for every (service, resource) group, find
// maximal runs of consecutive failing calls followed by the first successful
// call; each changed dimension between the last failure and the success
// becomes one candidate asset (auth change → strategy-switch, param
// change/addition → param-required). Pagination rule: a chain of ≥2
// successful calls on one resource whose params.cursor advances yields a
// pagination asset.
func ExtractExperiences(log []CallRecord, opt ExtractOptions) []*Asset {
	if !opt.Success || len(log) == 0 {
		return nil
	}
	type groupKey struct{ service, resource string }
	groups := map[groupKey][]CallRecord{}
	var order []groupKey
	for _, call := range log {
		if call.Tool != "api_call" || call.Service == "" || call.Resource == "" {
			continue
		}
		key := groupKey{call.Service, call.Resource}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], call)
	}

	var assets []*Asset
	seenKeys := map[string]bool{}
	addAsset := func(a *Asset) {
		if seenKeys[a.DedupKey] {
			return
		}
		seenKeys[a.DedupKey] = true
		assets = append(assets, a)
	}

	for _, key := range order {
		calls := groups[key]
		// §18.3 what-worked extraction: a group whose first call succeeds leaves
		// no failure→success trace, but the strategy that worked is still
		// experience. Record it at low confidence (0.40) and let independent
		// sessions mature it (0.55, 0.67, ...).
		if calls[0].OK && calls[0].Auth != "" {
			addAsset(whatWorkedAsset(key.service, key.resource, calls[0], opt))
		}
		for i := 0; i < len(calls); i++ {
			if calls[i].OK {
				continue
			}
			// Collect the failing run, then the first success after it.
			runEnd := i
			for runEnd+1 < len(calls) && !calls[runEnd+1].OK {
				runEnd++
			}
			if runEnd+1 >= len(calls) {
				break // failures never resolved within this task
			}
			success := calls[runEnd+1]
			failure := calls[runEnd]
			for _, asset := range diffAssets(key.service, key.resource, failure, success, i+1, runEnd-i+1, opt) {
				addAsset(asset)
			}
			i = runEnd + 1
		}
		// Pagination chain over successful calls.
		var chain []CallRecord
		for _, call := range calls {
			if call.OK && hasParam(call.Params, "cursor") {
				chain = append(chain, call)
			}
		}
		if len(chain) >= 2 && cursorAdvances(chain) {
			asset := paginationAsset(key.service, key.resource, chain, opt)
			addAsset(asset)
		}
	}
	return assets
}

// whatWorkedAsset records a first-attempt success at maturation baseline 0.40
// (§18.3): one lucky call must not become a high-confidence rule.
func whatWorkedAsset(service, resource string, call CallRecord, opt ExtractOptions) *Asset {
	now := nowUTC()
	return &Asset{
		DedupKey:    dedupKey(opt.Environment, service, resource, KindWhatWorked, "auth", call.Auth),
		Service:     service,
		Problem:     "auth",
		Kind:        KindWhatWorked,
		Proposition: fmt.Sprintf("on %s %s (environment %s) auth=%q worked on the first attempt", service, resource, opt.Environment, call.Auth),
		Recommendation: Recommendation{
			Service: service, Resource: resource,
			Param: "auth", Value: call.Auth, ValueType: "string",
		},
		Evidence: []Evidence{
			evidenceOf(opt.SessionID, opt.TaskID, call),
		},
		Confidence:     WhatWorkedBaseline,
		Status:         StatusActive,
		CreatedAt:      now,
		Environment:    opt.Environment,
		VersionContext: opt.Version,
	}
}

// diffAssets compares the last failing call with the first succeeding call
// and emits one asset per changed dimension.
func diffAssets(service, resource string, failure, success CallRecord, firstFailSeq, failCount int, opt ExtractOptions) []*Asset {
	var assets []*Asset
	now := nowUTC()
	// Auth dimension.
	if failure.Auth != success.Auth {
		assets = append(assets, &Asset{
			DedupKey: dedupKey(opt.Environment, service, resource, KindStrategySwitch, "auth", success.Auth),
			Service:  service,
			Problem:  "auth",
			Kind:     KindStrategySwitch,
			Proposition: fmt.Sprintf("on %s %s (environment %s) auth=%q failed %d times with HTTP %d; switching auth=%q succeeded",
				service, resource, opt.Environment, failure.Auth, failCount, failure.Status, success.Auth),
			Recommendation: Recommendation{
				Service: service, Resource: resource,
				Param: "auth", Value: success.Auth,
				Avoid: failure.Auth, AvoidStatus: failure.Status,
			},
			Evidence: []Evidence{
				evidenceOf(opt.SessionID, opt.TaskID, failure),
				evidenceOf(opt.SessionID, opt.TaskID, success),
			},
			Confidence:     0.7,
			Status:         StatusActive,
			CreatedAt:      now,
			Environment:    opt.Environment,
			VersionContext: opt.Version,
		})
	}
	// Param dimensions: keys present/changed between failure and success.
	for _, key := range sortedParamKeys(success.Params) {
		successValue := paramValue(success.Params, key)
		failureValue, had := lookupParam(failure.Params, key)
		if had && failureValue == successValue {
			continue
		}
		if key == "cursor" || key == "query" || key == "id" {
			continue // request data, not strategy
		}
		if !had {
			raw := lookupRaw(success.Params, key)
			assets = append(assets, &Asset{
				DedupKey: dedupKey(opt.Environment, service, resource, KindParamRequired, "params."+key, fmt.Sprintf("%v", successValue)),
				Service:  service,
				Problem:  "request-params",
				Kind:     KindParamRequired,
				Proposition: fmt.Sprintf("%s %s (environment %s) rejects requests without params.%s (HTTP %d); adding params.%s=%v succeeded",
					service, resource, opt.Environment, key, failure.Status, key, plainValue(success.Params, key)),
				Recommendation: Recommendation{
					Service: service, Resource: resource,
					Param: "params." + key, Value: fmt.Sprintf("%v", successValue),
					ValueType: jsonValueType(raw),
				},
				Evidence: []Evidence{
					evidenceOf(opt.SessionID, opt.TaskID, failure),
					evidenceOf(opt.SessionID, opt.TaskID, success),
				},
				Confidence:     0.7,
				Status:         StatusActive,
				CreatedAt:      now,
				Environment:    opt.Environment,
				VersionContext: opt.Version,
			})
		}
	}
	return assets
}

func paginationAsset(service, resource string, chain []CallRecord, opt ExtractOptions) *Asset {
	now := nowUTC()
	return &Asset{
		DedupKey:    dedupKey(opt.Environment, service, resource, KindPagination, "params.cursor", "iterate"),
		Service:     service,
		Problem:     "pagination",
		Kind:        KindPagination,
		Proposition: fmt.Sprintf("%s %s (environment %s) returns paginated results; iterate params.cursor using next_cursor from each response until the target is found", service, resource, opt.Environment),
		Recommendation: Recommendation{
			Service: service, Resource: resource,
			Param: "params.cursor", Value: "iterate",
		},
		Evidence: []Evidence{
			evidenceOf(opt.SessionID, opt.TaskID, chain[0]),
			evidenceOf(opt.SessionID, opt.TaskID, chain[len(chain)-1]),
		},
		Confidence:     0.55,
		Status:         StatusActive,
		CreatedAt:      now,
		Environment:    opt.Environment,
		VersionContext: opt.Version,
	}
}

func evidenceOf(sessionID, taskID string, call CallRecord) Evidence {
	return Evidence{
		SessionID: sessionID,
		TaskID:    taskID,
		Seq:       call.Seq,
		Tool:      call.Tool,
		Status:    call.Status,
		Summary:   call.Summary,
	}
}

func hasParam(params map[string]any, key string) bool {
	if params == nil {
		return false
	}
	_, ok := params[key]
	return ok
}

func cursorAdvances(chain []CallRecord) bool {
	for i := 1; i < len(chain); i++ {
		if paramValue(chain[i].Params, "cursor") == paramValue(chain[i-1].Params, "cursor") {
			return false
		}
	}
	return true
}

func sortedParamKeys(params map[string]any) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func lookupParam(params map[string]any, key string) (any, bool) {
	if params == nil {
		return nil, false
	}
	v, ok := params[key]
	return v, ok
}

func lookupRaw(params map[string]any, key string) any {
	v, _ := lookupParam(params, key)
	return v
}

func paramValue(params map[string]any, key string) string {
	if v, ok := lookupParam(params, key); ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func plainValue(params map[string]any, key string) any {
	if v, ok := lookupParam(params, key); ok {
		return v
	}
	return ""
}

func nowUTC() time.Time { return timeNow() }

// Maturation schedule for what-worked assets (§18.3): 0.40 baseline, then
// 0.55 / 0.67 from independent sessions, then the standard asymptote.
const (
	WhatWorkedBaseline = 0.40
	WhatWorkedMature1  = 0.55
	WhatWorkedMature2  = 0.67
)

// MatureConfidence returns the confidence after one more independent-session
// confirmation, by kind. what-worked-style assets follow the explicit schedule
// from the spec; failure→success assets keep the evidence-weighted asymptote.
func MatureConfidence(kind string, conf float64, confirmationCount int) float64 {
	switch kind {
	case KindWhatWorked, KindTestCommand, KindNav:
		switch confirmationCount {
		case 1:
			return WhatWorkedMature1 // 0.40 → 0.55 after first independent confirmation
		case 2:
			return WhatWorkedMature2 // 0.55 → 0.67
		default:
			return min64(0.95, conf+(0.95-conf)*0.15)
		}
	default:
		return min64(0.95, conf+(0.95-conf)*0.15)
	}
}

// timeNow is swappable for deterministic tests.
var timeNow = time.Now

// HintText renders the short hint line for an asset (§9, §15: a few lines,
// no transcripts).
func HintText(a *Asset) string {
	var b strings.Builder
	sessionWord := "sessions"
	if a.ConfirmationCount == 1 {
		sessionWord = "session"
	}
	env := ""
	if a.Environment != "" {
		env = " [env: " + a.Environment
		if a.VersionContext != "" {
			env += " " + a.VersionContext
		}
		env += "]"
	}
	fmt.Fprintf(&b, "%s%s | %s | confidence %.2f, confirmed in %d %s",
		a.Service, env, a.Proposition, a.Confidence, a.ConfirmationCount, sessionWord)
	return b.String()
}
