package cognition_test

import (
	"testing"

	"github.com/temporality-project/temporality/frp/cognition"
)

func TestClaimTransitionGraph(t *testing.T) {
	allowed := [][2]cognition.ClaimStatus{{cognition.ClaimCandidate, cognition.ClaimSupported}, {cognition.ClaimCandidate, cognition.ClaimRefuted}, {cognition.ClaimCandidate, cognition.ClaimSuperseded}, {cognition.ClaimSupported, cognition.ClaimRefuted}, {cognition.ClaimSupported, cognition.ClaimSuperseded}}
	for _, edge := range allowed {
		if !cognition.CanTransition(edge[0], edge[1]) {
			t.Errorf("expected transition %s -> %s", edge[0], edge[1])
		}
	}
	for _, terminal := range []cognition.ClaimStatus{cognition.ClaimRefuted, cognition.ClaimSuperseded} {
		for _, target := range []cognition.ClaimStatus{cognition.ClaimSupported, cognition.ClaimRefuted, cognition.ClaimSuperseded} {
			if cognition.CanTransition(terminal, target) {
				t.Errorf("terminal transition allowed: %s -> %s", terminal, target)
			}
		}
	}
}
