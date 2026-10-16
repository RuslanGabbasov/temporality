package workspace

import (
	"encoding/json"
	"strings"
	"testing"
)

func teamManifestJSON(t *testing.T, m TeamManifest) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return raw
}

func validSlot(id string) TeamSlot {
	return TeamSlot{ID: id, Title: strings.Title(id), Binding: TeamBinding{Mode: TeamBindingFixed, AgentID: id + "-agent"}}
}

func TestValidateTeamManifest(t *testing.T) {
	dagSteps := func() []TeamStep {
		return []TeamStep{
			{ID: "impl", SlotID: "coder"},
			{ID: "review", SlotID: "reviewer", ReviewOf: []string{"impl"}},
			{ID: "verify", SlotID: "reviewer", DependsOn: []string{"impl"}, ReviewOf: []string{"impl"}},
		}
	}
	cases := []struct {
		name    string
		mutate  func(*TeamManifest)
		wantErr string
	}{
		{"pipeline ok", func(m *TeamManifest) { m.Protocol.Kind = TeamProtocolPipeline }, ""},
		{"lead_workers ok", func(m *TeamManifest) { m.Protocol.Kind = TeamProtocolLeadWorkers }, ""},
		{"fan_out ok", func(m *TeamManifest) {
			m.Protocol.Kind = TeamProtocolFanOut
			m.Slots = append(m.Slots, TeamSlot{ID: "reducer", Binding: TeamBinding{Mode: TeamBindingRole}})
		}, ""},
		{"review_gate ok", func(m *TeamManifest) { m.Protocol.Kind = TeamProtocolReviewGate }, ""},
		{"dag ok", func(m *TeamManifest) { m.Protocol.Kind = TeamProtocolDAG; m.Protocol.Steps = dagSteps() }, ""},
		{"marshal of zero struct reports missing kind", func(m *TeamManifest) { *m = TeamManifest{} }, "protocol.kind is required"},
		{"missing protocol kind", func(m *TeamManifest) { m.Protocol.Kind = "" }, "protocol.kind is required"},
		{"unknown protocol kind", func(m *TeamManifest) { m.Protocol.Kind = "roundtable" }, "unknown protocol kind"},
		{"no slots", func(m *TeamManifest) { m.Slots = nil }, "at least one slot"},
		{"duplicate slot ids", func(m *TeamManifest) { m.Slots = append(m.Slots, validSlot("coder")) }, "duplicate slot id"},
		{"empty slot id", func(m *TeamManifest) { m.Slots[0].ID = " " }, "slot[0].id is required"},
		{"fixed without agent", func(m *TeamManifest) { m.Slots[0].Binding = TeamBinding{Mode: TeamBindingFixed} }, "fixed binding requires agent_id"},
		{"role binding ok without preferred", func(m *TeamManifest) { m.Slots[0].Binding = TeamBinding{Mode: TeamBindingRole} }, ""},
		{"unknown binding mode", func(m *TeamManifest) { m.Slots[0].Binding = TeamBinding{Mode: "auto"} }, "unknown binding mode"},
		{"role slot with unknown capability", func(m *TeamManifest) {
			m.Slots[0].Binding = TeamBinding{Mode: TeamBindingRole}
			m.Slots[0].Requirements = TeamRequirements{Capabilities: []string{"telepathy"}}
		}, "unknown capability"},
		{"role slot with capability alias write_files", func(m *TeamManifest) {
			m.Slots[0].Binding = TeamBinding{Mode: TeamBindingRole}
			m.Slots[0].Requirements = TeamRequirements{Capabilities: []string{"write_files"}}
		}, ""},
		{"role slot with empty skill requirement", func(m *TeamManifest) {
			m.Slots[0].Binding = TeamBinding{Mode: TeamBindingRole}
			m.Slots[0].Requirements = TeamRequirements{Skills: []string{" "}}
		}, "empty skill requirement"},
		{"review_gate single slot", func(m *TeamManifest) {
			m.Protocol.Kind = TeamProtocolReviewGate
			m.Slots = m.Slots[:1]
		}, "at least two slots"},
		{"fan_out single slot", func(m *TeamManifest) {
			m.Protocol.Kind = TeamProtocolFanOut
			m.Slots = m.Slots[:1]
		}, "worker slots plus a reducer"},
		{"dag without steps", func(m *TeamManifest) { m.Protocol.Kind = TeamProtocolDAG }, "requires steps"},
		{"dag step unknown slot", func(m *TeamManifest) {
			m.Protocol.Kind = TeamProtocolDAG
			m.Protocol.Steps = []TeamStep{{ID: "x", SlotID: "ghost"}}
		}, "unknown slot"},
		{"dag cycle", func(m *TeamManifest) {
			m.Protocol.Kind = TeamProtocolDAG
			m.Protocol.Steps = []TeamStep{
				{ID: "a", SlotID: "coder", DependsOn: []string{"b"}},
				{ID: "b", SlotID: "reviewer", DependsOn: []string{"a"}},
			}
		}, "cycle"},
		{"dag dangling dependency", func(m *TeamManifest) {
			m.Protocol.Kind = TeamProtocolDAG
			m.Protocol.Steps = []TeamStep{{ID: "a", SlotID: "coder", DependsOn: []string{"missing"}}}
		}, "unknown step"},
		{"dag review of unknown step", func(m *TeamManifest) {
			m.Protocol.Kind = TeamProtocolDAG
			m.Protocol.Steps = []TeamStep{{ID: "a", SlotID: "reviewer", ReviewOf: []string{"ghost"}}}
		}, "reviews unknown step"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := TeamManifest{
				Protocol: TeamProtocol{Kind: TeamProtocolPipeline},
				Slots:    []TeamSlot{validSlot("coder"), validSlot("reviewer")},
			}
			tc.mutate(&m)
			err := ValidateTeamManifest(teamManifestJSON(t, m))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateTeamManifestInvalidJSON(t *testing.T) {
	if err := ValidateTeamManifest([]byte("{not json")); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("error = %v, want JSON parse failure", err)
	}
}

func TestValidateTeamManifestEmptyBody(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(""), []byte("  "), []byte("{}")} {
		if err := ValidateTeamManifest(raw); err == nil {
			t.Fatalf("ValidateTeamManifest(%q) = nil, want error", raw)
		}
	}
}

func TestTeamManifestStringShorthands(t *testing.T) {
	// Models and hand-written manifests often collapse a struct with one
	// dominant field onto a plain string; the unmarshalers must accept both.
	raw := []byte(`{
		"protocol": "review_gate",
		"slots": [
			{ "id": "coder", "title": "Coder", "binding": { "mode": "fixed", "agent_id": "coder-1" } },
			{ "id": "reviewer", "title": "Reviewer", "binding": "role" }
		]
	}`)
	var manifest TeamManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal with string shorthands: %v", err)
	}
	if manifest.Protocol.Kind != "review_gate" || manifest.Protocol.MaxRework != 0 || manifest.Protocol.Steps != nil {
		t.Fatalf("protocol = %+v, want kind-only value", manifest.Protocol)
	}
	if manifest.Slots[1].Binding.Mode != "role" || manifest.Slots[1].Binding.AgentID != "" {
		t.Fatalf("binding = %+v, want mode-only value", manifest.Slots[1].Binding)
	}
	// The canonical object form keeps working and round-trips unchanged.
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again TeamManifest
	if err := json.Unmarshal(canonical, &again); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if again.Protocol.Kind != manifest.Protocol.Kind || again.Slots[1].Binding.Mode != manifest.Slots[1].Binding.Mode {
		t.Fatalf("round-trip changed the manifest: %+v vs %+v", again, manifest)
	}
	if err := ValidateTeamManifest(canonical); err != nil {
		t.Fatalf("validate canonical form: %v", err)
	}
}
