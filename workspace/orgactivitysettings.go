package workspace

// Organizational Operations node settings (docs/temporality-organizational-
// operations-spec.md §5): independent per-node toggles with local /
// inherited / system value semantics. Storage mirrors org_policy — rows on
// (org_unit_id, key), org_unit_id '' is the installation-wide system row.
// Resolution walks the unit's ancestor chain self→root, the nearest explicit
// value wins, the system row is the fallback and the spec defaults come last.
// Sources are returned with the effective values so the UI can display where
// every value came from (spec §5: «UI должен показывать источник настройки»).

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Setting keys (spec §5 table).
const (
	OrgSettingActivityEnabled              = "activity_enabled"
	OrgSettingActivityBoardEnabled         = "activity_board_enabled"
	OrgSettingAgentAwarenessEnabled        = "agent_awareness_enabled"
	OrgSettingManagementViewEnabled        = "management_view_enabled"
	OrgSettingPublishRunStarts             = "publish_run_starts"
	OrgSettingPublishRunCompletions        = "publish_run_completions"
	OrgSettingPublishRunFailures           = "publish_run_failures"
	OrgSettingPublishSignificantProgress   = "publish_significant_progress"
	OrgSettingPublishArtifactEvents        = "publish_artifact_events"
	OrgSettingPublishQualityEvents         = "publish_quality_events"
	OrgSettingAllowTraceDiscovery          = "allow_trace_discovery"
	OrgSettingAwarenessScope               = "awareness_scope"
	OrgSettingAwarenessScopeUnits          = "awareness_scope_units"
	OrgSettingBatchIntervalMinutes         = "batch_interval_minutes"
	OrgSettingUrgentProcessingEnabled      = "urgent_processing_enabled"
	OrgSettingMaxEventsPerBatch            = "max_events_per_batch"
	OrgSettingMaxRecipientsPerEvent        = "max_recipients_per_event"
	OrgSettingMaxDeliveriesPerRunPerWindow = "max_deliveries_per_run_per_window"
	OrgSettingActivityRetention            = "activity_retention"
	OrgSettingDeliveryRetention            = "delivery_retention"
)

// Awareness scope values (spec §5: собственный узел; узел и дочерние; узел и
// явно разрешённые смежные).
const (
	OrgAwarenessScopeUnit    = "unit"
	OrgAwarenessScopeSubtree = "subtree"
	OrgAwarenessScopeCustom  = "custom"
)

// orgSettingKind is the value grammar of one key.
type orgSettingKind string

const (
	orgSettingBool     orgSettingKind = "bool"
	orgSettingInt      orgSettingKind = "int"
	orgSettingDuration orgSettingKind = "duration"
	orgSettingEnum     orgSettingKind = "enum"
	orgSettingList     orgSettingKind = "list"
)

type orgSettingSpec struct {
	kind    orgSettingKind
	def     string
	allowed []string // enum values
	minimum int      // int keys
	maximum int
}

// orgActivitySettingSpecs fixes the grammar and defaults. Defaults are the
// safe minimum: collection is on (the boards depend on it), the LLM-backed
// supervisor is OFF until a node explicitly enables it (cost), scope is the
// tightest unit.
var orgActivitySettingSpecs = map[string]orgSettingSpec{
	OrgSettingActivityEnabled:              {kind: orgSettingBool, def: "true"},
	OrgSettingActivityBoardEnabled:         {kind: orgSettingBool, def: "true"},
	OrgSettingAgentAwarenessEnabled:        {kind: orgSettingBool, def: "false"},
	OrgSettingManagementViewEnabled:        {kind: orgSettingBool, def: "true"},
	OrgSettingPublishRunStarts:             {kind: orgSettingBool, def: "true"},
	OrgSettingPublishRunCompletions:        {kind: orgSettingBool, def: "true"},
	OrgSettingPublishRunFailures:           {kind: orgSettingBool, def: "true"},
	OrgSettingPublishSignificantProgress:   {kind: orgSettingBool, def: "true"},
	OrgSettingPublishArtifactEvents:        {kind: orgSettingBool, def: "true"},
	OrgSettingPublishQualityEvents:         {kind: orgSettingBool, def: "true"},
	OrgSettingAllowTraceDiscovery:          {kind: orgSettingBool, def: "true"},
	OrgSettingAwarenessScope:               {kind: orgSettingEnum, def: OrgAwarenessScopeUnit, allowed: []string{OrgAwarenessScopeUnit, OrgAwarenessScopeSubtree, OrgAwarenessScopeCustom}},
	OrgSettingAwarenessScopeUnits:          {kind: orgSettingList, def: ""},
	OrgSettingBatchIntervalMinutes:         {kind: orgSettingInt, def: "10", minimum: 1, maximum: 1440},
	OrgSettingUrgentProcessingEnabled:      {kind: orgSettingBool, def: "false"},
	OrgSettingMaxEventsPerBatch:            {kind: orgSettingInt, def: "200", minimum: 10, maximum: 5000},
	OrgSettingMaxRecipientsPerEvent:        {kind: orgSettingInt, def: "10", minimum: 1, maximum: 200},
	OrgSettingMaxDeliveriesPerRunPerWindow: {kind: orgSettingInt, def: "5", minimum: 1, maximum: 100},
	OrgSettingActivityRetention:            {kind: orgSettingDuration, def: "2160h"}, // 90 days
	OrgSettingDeliveryRetention:            {kind: orgSettingDuration, def: "720h"},  // 30 days
}

// validateOrgSettingText checks one raw value against its spec.
func validateOrgSettingText(key, value string) error {
	spec, ok := orgActivitySettingSpecs[key]
	if !ok {
		return fmt.Errorf("unknown org activity setting %q", key)
	}
	value = strings.TrimSpace(value)
	switch spec.kind {
	case orgSettingBool:
		if value != "true" && value != "false" {
			return fmt.Errorf("setting %q must be true or false", key)
		}
	case orgSettingInt:
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
			return fmt.Errorf("setting %q must be an integer", key)
		}
		if n < spec.minimum || n > spec.maximum {
			return fmt.Errorf("setting %q must be between %d and %d", key, spec.minimum, spec.maximum)
		}
	case orgSettingDuration:
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("setting %q must be a duration (e.g. 720h)", key)
		}
	case orgSettingEnum:
		for _, allowed := range spec.allowed {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("setting %q must be one of: %s", key, strings.Join(spec.allowed, ", "))
	case orgSettingList:
		// comma-separated unit ids; empty is fine (no adjacency grants)
		for _, unit := range strings.Split(value, ",") {
			if strings.TrimSpace(unit) == "" && len(strings.Split(value, ",")) > 1 {
				return fmt.Errorf("setting %q has an empty list entry", key)
			}
		}
	}
	return nil
}

// OrgSettingSource records where one effective value came from (spec §5).
type OrgSettingSource struct {
	Key    string `json:"key"`
	UnitID string `json:"org_unit_id"` // '' = system row
	Origin string `json:"origin"`      // local|inherited|system|default
	Value  string `json:"value"`
}

// OrgActivitySettings is the resolved, typed view of a node's settings.
type OrgActivitySettings struct {
	ActivityEnabled              bool
	ActivityBoardEnabled         bool
	AgentAwarenessEnabled        bool
	ManagementViewEnabled        bool
	PublishRunStarts             bool
	PublishRunCompletions        bool
	PublishRunFailures           bool
	PublishSignificantProgress   bool
	PublishArtifactEvents        bool
	PublishQualityEvents         bool
	AllowTraceDiscovery          bool
	AwarenessScope               string
	AwarenessScopeUnits          []string
	BatchIntervalMinutes         int
	UrgentProcessingEnabled      bool
	MaxEventsPerBatch            int
	MaxRecipientsPerEvent        int
	MaxDeliveriesPerRunPerWindow int
	ActivityRetention            time.Duration
	DeliveryRetention            time.Duration
}

// OrgActivitySettingsResult pairs the effective settings with their sources
// (one entry per key, keyed by setting key).
type OrgActivitySettingsResult struct {
	Settings OrgActivitySettings            `json:"settings"`
	Values   map[string]string              `json:"values"`
	Sources  map[string]OrgSettingSource    `json:"sources"`
}

// resolveOrgActivitySettings is the pure inheritance resolver: rows maps
// unit id → key → raw value; chain is the unit's ancestor chain oldest first
// (UnitChain). The nearest explicit value wins (self first), the '' system
// row is the fallback, defaults close the gaps.
func resolveOrgActivitySettings(self string, chain []string, rows map[string]map[string]string) (map[string]string, map[string]OrgSettingSource) {
	// Nearest first: reverse the oldest-first chain (self, parent, …, root),
	// then the system row ('') — distance after every ancestor.
	order := make([]string, 0, len(chain)+1)
	seen := map[string]bool{}
	for i := len(chain) - 1; i >= 0; i-- {
		if !seen[chain[i]] {
			order = append(order, chain[i])
			seen[chain[i]] = true
		}
	}
	order = append(order, "")

	values := make(map[string]string, len(orgActivitySettingSpecs))
	sources := make(map[string]OrgSettingSource, len(orgActivitySettingSpecs))
	for key, spec := range orgActivitySettingSpecs {
		values[key] = spec.def
		sources[key] = OrgSettingSource{Key: key, UnitID: "", Origin: "default", Value: spec.def}
	}
	// Units are visited nearest-first, so the FIRST explicit row for a key
	// wins; later rows never overwrite it. Only the default placeholder can
	// still be replaced.
	for _, unit := range order {
		unitRows := rows[unit]
		if len(unitRows) == 0 {
			continue
		}
		for key, value := range unitRows {
			if _, ok := orgActivitySettingSpecs[key]; !ok {
				continue // unknown key: never let it shadow the grammar
			}
			if sources[key].Origin != "default" {
				continue // a nearer row already resolved this key
			}
			origin := "inherited"
			switch {
			case self != "" && unit == self:
				origin = "local"
			case unit == "":
				origin = "system"
			}
			values[key] = value
			sources[key] = OrgSettingSource{Key: key, UnitID: unit, Origin: origin, Value: value}
		}
	}
	return values, sources
}

func orgSettingSpecInfo(key string) (orgSettingSpec, string, bool) {
	spec, ok := orgActivitySettingSpecs[key]
	if !ok {
		return orgSettingSpec{}, "", false
	}
	return spec, spec.def, true
}

// OrgActivitySettingsFor builds the typed settings from resolved raw values.
func OrgActivitySettingsFor(values map[string]string) OrgActivitySettings {
	boolAt := func(key string) bool { return values[key] == "true" }
	intAt := func(key string) int {
		var n int
		_, _ = fmt.Sscanf(values[key], "%d", &n)
		return n
	}
	durationAt := func(key string) time.Duration {
		d, err := time.ParseDuration(values[key])
		if err != nil {
			return 0
		}
		return d
	}
	settings := OrgActivitySettings{
		ActivityEnabled:              boolAt(OrgSettingActivityEnabled),
		ActivityBoardEnabled:         boolAt(OrgSettingActivityBoardEnabled),
		AgentAwarenessEnabled:        boolAt(OrgSettingAgentAwarenessEnabled),
		ManagementViewEnabled:        boolAt(OrgSettingManagementViewEnabled),
		PublishRunStarts:             boolAt(OrgSettingPublishRunStarts),
		PublishRunCompletions:        boolAt(OrgSettingPublishRunCompletions),
		PublishRunFailures:           boolAt(OrgSettingPublishRunFailures),
		PublishSignificantProgress:   boolAt(OrgSettingPublishSignificantProgress),
		PublishArtifactEvents:        boolAt(OrgSettingPublishArtifactEvents),
		PublishQualityEvents:         boolAt(OrgSettingPublishQualityEvents),
		AllowTraceDiscovery:          boolAt(OrgSettingAllowTraceDiscovery),
		AwarenessScope:               values[OrgSettingAwarenessScope],
		BatchIntervalMinutes:         intAt(OrgSettingBatchIntervalMinutes),
		UrgentProcessingEnabled:      boolAt(OrgSettingUrgentProcessingEnabled),
		MaxEventsPerBatch:            intAt(OrgSettingMaxEventsPerBatch),
		MaxRecipientsPerEvent:        intAt(OrgSettingMaxRecipientsPerEvent),
		MaxDeliveriesPerRunPerWindow: intAt(OrgSettingMaxDeliveriesPerRunPerWindow),
		ActivityRetention:            durationAt(OrgSettingActivityRetention),
		DeliveryRetention:            durationAt(OrgSettingDeliveryRetention),
	}
	settings.AwarenessScopeUnits = parseOrgSettingList(values[OrgSettingAwarenessScopeUnits])
	return settings
}

func parseOrgSettingList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if unit := strings.TrimSpace(part); unit != "" {
			out = append(out, unit)
		}
	}
	return out
}

// SetOrgActivitySetting upserts one validated local (or system, unitID='')
// setting row.
func (s *Store) SetOrgActivitySetting(ctx context.Context, unitID, key, value string) error {
	if _, _, ok := orgSettingSpecInfo(key); !ok {
		return fmt.Errorf("unknown org activity setting %q", key)
	}
	value = strings.TrimSpace(value)
	if err := validateOrgSettingText(key, value); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_activity_setting (org_unit_id, key, value, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (org_unit_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		unitID, key, value)
	return err
}

// DeleteOrgActivitySetting removes the local override so the value inherits
// again.
func (s *Store) DeleteOrgActivitySetting(ctx context.Context, unitID, key string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM org_activity_setting WHERE org_unit_id = $1 AND key = $2`, unitID, key)
	return err
}

// OrgActivitySettingRows lists the raw rows for one unit (its local values).
func (s *Store) OrgActivitySettingRows(ctx context.Context, unitID string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key, value FROM org_activity_setting WHERE org_unit_id = $1`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}

// orgActivitySettingRowsByUnits loads the raw rows for every given unit id in
// one query and groups them per unit.
func (s *Store) orgActivitySettingRowsByUnits(ctx context.Context, unitIDs []string) (map[string]map[string]string, error) {
	rows := make(map[string]map[string]string, len(unitIDs)+1)
	if len(unitIDs) == 0 {
		return rows, nil
	}
	result, err := s.pool.Query(ctx,
		`SELECT org_unit_id, key, value FROM org_activity_setting WHERE org_unit_id = ANY($1)`, textListArg(unitIDs))
	if err != nil {
		return nil, err
	}
	defer result.Close()
	for result.Next() {
		var unit, key, value string
		if err := result.Scan(&unit, &key, &value); err != nil {
			return nil, err
		}
		if rows[unit] == nil {
			rows[unit] = map[string]string{}
		}
		rows[unit][key] = value
	}
	return rows, result.Err()
}

// EffectiveOrgActivitySettings resolves a node's settings through its
// ancestor chain. Sources answer «where did this value come from».
func (s *Store) EffectiveOrgActivitySettings(ctx context.Context, unitID string) (OrgActivitySettingsResult, error) {
	result := OrgActivitySettingsResult{
		Values:  map[string]string{},
		Sources: map[string]OrgSettingSource{},
	}
	chain, err := s.UnitChain(ctx, unitID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			chain = []string{unitID} // stale unit: resolve self-only, no inherit
		} else {
			return result, err
		}
	}
	// UnitChain('') returns nil — the system view resolves from the '' row
	// alone; a concrete unit always resolves through its chain.
	lookupUnits := chain
	if unitID == "" {
		lookupUnits = []string{""}
	}
	rowsByUnit, err := s.orgActivitySettingRowsByUnits(ctx, lookupUnits)
	if err != nil {
		return result, err
	}
	values, sources := resolveOrgActivitySettings(unitID, chain, rowsByUnit)
	result.Values = values
	result.Sources = sources
	result.Settings = OrgActivitySettingsFor(values)
	return result, nil
}

// OrgActivitySettingKeys lists the known setting keys (the settings UI
// grammar).
func OrgActivitySettingKeys() []string {
	keys := make([]string, 0, len(orgActivitySettingSpecs))
	for key := range orgActivitySettingSpecs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// AllOrgActivitySettingRows loads every settings row (all units + system).
// The fold loop resolves every node from one query per pass.
func (s *Store) AllOrgActivitySettingRows(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT org_unit_id, key, value FROM org_activity_setting`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]map[string]string{}
	for rows.Next() {
		var unit, key, value string
		if err := rows.Scan(&unit, &key, &value); err != nil {
			return nil, err
		}
		if result[unit] == nil {
			result[unit] = map[string]string{}
		}
		result[unit][key] = value
	}
	return result, rows.Err()
}

// ResolveOrgActivitySettingsForUnits computes the effective settings for
// every unit (and the '' system view) from the unit list and the raw rows —
// pure, so tests cover the inheritance matrix without a database.
func ResolveOrgActivitySettingsForUnits(units []OrgUnit, rowsByUnit map[string]map[string]string) map[string]OrgActivitySettingsResult {
	out := map[string]OrgActivitySettingsResult{}
	values, sources := resolveOrgActivitySettings("", nil, rowsByUnit)
	out[""] = resultFrom(values, sources)
	for _, unit := range units {
		values, sources := resolveOrgActivitySettings(unit.ID, unit.Ancestors(), rowsByUnit)
		out[unit.ID] = resultFrom(values, sources)
	}
	return out
}

func resultFrom(values map[string]string, sources map[string]OrgSettingSource) OrgActivitySettingsResult {
	return OrgActivitySettingsResult{Values: values, Sources: sources, Settings: OrgActivitySettingsFor(values)}
}
