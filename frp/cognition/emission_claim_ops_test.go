package cognition_test

import (
	"encoding/json"
	"testing"

	"github.com/temporality-project/temporality/frp/cognition"
)

func claimOpsEmission(t *testing.T, raw string) cognition.CognitiveEmission {
	t.Helper()
	var emission cognition.CognitiveEmission
	if err := json.Unmarshal([]byte(raw), &emission); err != nil {
		t.Fatal(err)
	}
	return emission
}

func TestClaimOpsDecodeAcceptsStringAndObjectRefs(t *testing.T) {
	emission := claimOpsEmission(t, `{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"f","claim_ops":[{"op":"refute","claim":"claim:00000000-0000-4000-8000-000000000001"},{"op":"refute","claim":{"type":"claim","id":"00000000-0000-4000-8000-000000000002"}}],"claims":[{"proposition":"corrected","confidence":0.9,"status":"candidate","supersedes":{"type":"claim","id":"00000000-0000-4000-8000-000000000003"}}]}`)
	if len(emission.ClaimOps) != 2 {
		t.Fatalf("claim_ops = %#v", emission.ClaimOps)
	}
	if emission.ClaimOps[0].Claim != "claim:00000000-0000-4000-8000-000000000001" {
		t.Fatalf("string ref not preserved: %q", emission.ClaimOps[0].Claim)
	}
	if emission.ClaimOps[1].Claim != "claim:00000000-0000-4000-8000-000000000002" {
		t.Fatalf("object ref not normalized: %q", emission.ClaimOps[1].Claim)
	}
	if emission.Claims[0].Supersedes != "claim:00000000-0000-4000-8000-000000000003" {
		t.Fatalf("supersedes object ref not normalized: %q", emission.Claims[0].Supersedes)
	}
	emission.ApplyDefaults()
	if err := emission.Validate(); err != nil {
		t.Fatalf("valid claim ops rejected: %v", err)
	}
}

func TestClaimOpsValidateRejectsUnknownOpAndWrongRefType(t *testing.T) {
	base := cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "e", FrameID: "f", ClaimOps: []cognition.ClaimOperation{{Op: "support", Claim: "claim:00000000-0000-4000-8000-000000000001"}}}
	if err := base.Validate(); err == nil {
		t.Fatal("unknown claim op accepted")
	}
	base.ClaimOps = []cognition.ClaimOperation{{Op: "refute", Claim: "event:00000000-0000-4000-8000-000000000001"}}
	if err := base.Validate(); err == nil {
		t.Fatal("non-claim ref accepted")
	}
	base.ClaimOps = []cognition.ClaimOperation{{Op: "confirm", Claim: "event:00000000-0000-4000-8000-000000000001"}}
	if err := base.Validate(); err == nil {
		t.Fatal("confirm with non-claim ref accepted")
	}
	base.ClaimOps = nil
	base.Claims = []cognition.EmittedClaim{{Proposition: "p", Confidence: 0.5, Status: cognition.ClaimCandidate, Supersedes: "event:00000000-0000-4000-8000-000000000001"}}
	if err := base.Validate(); err == nil {
		t.Fatal("supersedes with non-claim ref accepted")
	}
}

// TestClaimLifecycleDecodeAndValidate pins the pivot knowledge-lifecycle
// contract at the schema level: confirm is a first-class claim op, evidence
// normalizes string and object refs, a born-supported claim must cite events,
// and terminal birth statuses stay impossible.
func TestClaimLifecycleDecodeAndValidate(t *testing.T) {
	emission := claimOpsEmission(t, `{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"f","claims":[{"proposition":"main.go contains Sum","confidence":0.95,"status":"supported","evidence":["event:00000000-0000-4000-8000-000000000001",{"type":"event","id":"00000000-0000-4000-8000-000000000002"},"event:00000000-0000-4000-8000-000000000001",""]},{"proposition":"Sum bug causes the failure","confidence":0.7,"status":"candidate","evidence":["event:00000000-0000-4000-8000-000000000001"]}],"claim_ops":[{"op":"confirm","claim":"claim:00000000-0000-4000-8000-000000000003"}]}`)
	emission.ApplyDefaults()
	if err := emission.Validate(); err != nil {
		t.Fatalf("supported claim with evidence rejected: %v", err)
	}
	want := []string{"event:00000000-0000-4000-8000-000000000001", "event:00000000-0000-4000-8000-000000000002"}
	if got := emission.Claims[0].Evidence; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("evidence not normalized/deduped: %v", got)
	}

	bare := cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "e", FrameID: "f", Claims: []cognition.EmittedClaim{{Proposition: "unsupported fact", Confidence: 0.9, Status: cognition.ClaimSupported}}}
	if err := bare.Validate(); err == nil {
		t.Fatal("supported claim without evidence accepted")
	}
	bare.Claims[0].Status = cognition.ClaimRefuted
	if err := bare.Validate(); err == nil {
		t.Fatal("terminal birth status accepted")
	}
	bare.Claims[0].Status = cognition.ClaimCandidate
	bare.Claims[0].Evidence = []string{"claim:00000000-0000-4000-8000-000000000004"}
	if err := bare.Validate(); err == nil {
		t.Fatal("non-event evidence ref accepted")
	}
}

func TestClaimOpsApplyDefaultsDropsUnanchoredOps(t *testing.T) {
	emission := cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "e", FrameID: "f", ClaimOps: []cognition.ClaimOperation{{Op: "refute", Claim: "   "}, {Op: "refute", Claim: "claim:00000000-0000-4000-8000-000000000001"}}}
	emission.ApplyDefaults()
	if len(emission.ClaimOps) != 1 || emission.ClaimOps[0].Claim != "claim:00000000-0000-4000-8000-000000000001" {
		t.Fatalf("unanchored claim ops not dropped: %#v", emission.ClaimOps)
	}
	// Wire round trip keeps claim_ops durable for the stored emission JSON.
	emission.EmissionID = "e2"
	encoded, err := json.Marshal(emission)
	if err != nil {
		t.Fatal(err)
	}
	var decoded cognition.CognitiveEmission
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ClaimOps) != 1 {
		t.Fatalf("claim_ops lost in round trip: %s", encoded)
	}
}
