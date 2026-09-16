package cognition_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/protocol"
)

func tensionClaim(id, proposition string, status cognition.ClaimStatus, triple ...string) cognition.Claim {
	claim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: id, Proposition: proposition, Confidence: 0.8, Status: status, CreatedEvent: "event-" + id, ValidFrom: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	if len(triple) == 3 {
		claim.Subject, claim.Predicate, claim.Object = triple[0], triple[1], triple[2]
	}
	return claim
}

func TestAllTensionsGroupsDuplicatesByNormalizedProposition(t *testing.T) {
	claims := []cognition.Claim{
		tensionClaim("c1", "The structure is  unknown", cognition.ClaimCandidate),
		tensionClaim("c2", "the structure is unknown", cognition.ClaimSupported),
		tensionClaim("c3", "the structure is unknown", cognition.ClaimCandidate),
		tensionClaim("c4", "go.mod exists", cognition.ClaimCandidate),
	}
	tensions := cognition.AllTensions(claims)
	if len(tensions) != 1 {
		t.Fatalf("tensions = %#v, want one duplicate group", tensions)
	}
	group := tensions[0]
	if group.Kind != cognition.TensionDuplicate || !reflect.DeepEqual(group.ClaimIDs, []string{"c1", "c2", "c3"}) {
		t.Fatalf("unexpected group: %#v", group)
	}
	if group.Detail == "" {
		t.Fatal("duplicate detail must be human-readable")
	}
}

func TestAllTensionsDetectsFunctionalContradictionsOnly(t *testing.T) {
	claims := []cognition.Claim{
		// module:demo declared in two files — cannot both be the declaration site.
		tensionClaim("c1", "module declared in a.go", cognition.ClaimCandidate, "module:demo", "declared_in", "file:main.go"),
		tensionClaim("c2", "module declared in b.go", cognition.ClaimCandidate, "module:demo", "declared_in", "file:other.go"),
		// repo contains two files — contains is one-to-many, no tension.
		tensionClaim("c3", "repo contains a", cognition.ClaimCandidate, "repo:demo", "contains", "file:a.go"),
		tensionClaim("c4", "repo contains b", cognition.ClaimCandidate, "repo:demo", "contains", "file:b.go"),
		// same functional predicate and same object — agreement, no tension.
		tensionClaim("c5", "commit authored by alice", cognition.ClaimCandidate, "commit:x", "authored_by", "person:alice"),
		tensionClaim("c6", "commit authored by alice again", cognition.ClaimCandidate, "commit:x", "authored_by", "person:alice"),
	}
	tensions := cognition.AllTensions(claims)
	if len(tensions) != 1 {
		t.Fatalf("tensions = %#v, want exactly the declared_in contradiction", tensions)
	}
	if tensions[0].Kind != cognition.TensionContradiction || !reflect.DeepEqual(tensions[0].ClaimIDs, []string{"c1", "c2"}) {
		t.Fatalf("unexpected contradiction: %#v", tensions[0])
	}
}

func TestAllTensionsIgnoresInactiveClaims(t *testing.T) {
	claims := []cognition.Claim{
		tensionClaim("c1", "module declared in a.go", cognition.ClaimSuperseded, "module:demo", "declared_in", "file:main.go"),
		tensionClaim("c2", "module declared in b.go", cognition.ClaimCandidate, "module:demo", "declared_in", "file:other.go"),
		tensionClaim("c3", "retired proposition", cognition.ClaimRefuted),
		tensionClaim("c4", "retired proposition", cognition.ClaimRefuted),
	}
	if tensions := cognition.AllTensions(claims); len(tensions) != 0 {
		t.Fatalf("inactive claims must not tension: %#v", tensions)
	}
}

func TestNewTensionsReportsOnlyWhatTheStepIntroduced(t *testing.T) {
	existing := []cognition.Claim{
		tensionClaim("c1", "stale duplicate", cognition.ClaimCandidate),
		tensionClaim("c2", "stale duplicate", cognition.ClaimCandidate),
		tensionClaim("c3", "module declared in a.go", cognition.ClaimCandidate, "module:demo", "declared_in", "file:main.go"),
	}
	incoming := []cognition.Claim{
		tensionClaim("n1", "module declared in b.go", cognition.ClaimCandidate, "module:demo", "declared_in", "file:other.go"),
		tensionClaim("n2", "fresh claim", cognition.ClaimCandidate),
	}
	tensions := cognition.NewTensions(existing, incoming)
	if len(tensions) != 1 {
		t.Fatalf("tensions = %#v, want only the incoming contradiction", tensions)
	}
	if tensions[0].Kind != cognition.TensionContradiction || !reflect.DeepEqual(tensions[0].ClaimIDs, []string{"c3", "n1"}) {
		t.Fatalf("unexpected tension: %#v", tensions[0])
	}
	// An incoming claim duplicating only itself (two copies in one emission)
	// is still a tension the step introduced.
	selfDuplicate := cognition.NewTensions(nil, []cognition.Claim{
		tensionClaim("n3", "echo", cognition.ClaimCandidate),
		tensionClaim("n4", "Echo", cognition.ClaimCandidate),
	})
	if len(selfDuplicate) != 1 || selfDuplicate[0].Kind != cognition.TensionDuplicate {
		t.Fatalf("intra-emission duplicate missing: %#v", selfDuplicate)
	}
}

func TestTensionOutputIsDeterministic(t *testing.T) {
	claims := []cognition.Claim{
		tensionClaim("c2", "dup", cognition.ClaimCandidate),
		tensionClaim("c1", "dup", cognition.ClaimCandidate),
		tensionClaim("c3", "branch", cognition.ClaimCandidate, "repo:x", "on_branch", "branch:main"),
		tensionClaim("c4", "other branch", cognition.ClaimCandidate, "repo:x", "on_branch", "branch:dev"),
	}
	first := cognition.AllTensions(claims)
	second := cognition.AllTensions([]cognition.Claim{claims[2], claims[3], claims[1], claims[0]})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("tension order depends on input order:\n%#v\n%#v", first, second)
	}
}
