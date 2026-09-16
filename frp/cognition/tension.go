package cognition

import (
	"fmt"
	"sort"
	"strings"

	"github.com/temporality-project/temporality/frp/entity"
)

// M16 memory reliability: claims are cheap for a model to emit and expensive
// for a substrate to hold. Nothing here decides which claim is right — the
// runtime does not resolve contradictions silently. It groups the claims that
// cannot all be trusted at once and surfaces them, so the model (and the
// debugger) see the tension instead of a memory that quietly rots.

type MemoryTensionKind string

const (
	// TensionDuplicate: the same proposition asserted by several active
	// claims. Harmless once, pathological in a loop — every step re-emitting
	// "the structure is unknown" grows the substrate without growing knowledge.
	TensionDuplicate MemoryTensionKind = "duplicate"
	// TensionContradiction: active claims binding one subject through a
	// functional predicate to different objects. Both cannot be true.
	TensionContradiction MemoryTensionKind = "contradiction"
)

// MemoryTension names a group of active claims the substrate currently holds
// that cannot all be trusted at once. ClaimIDs are sorted; the group is the
// unit of reporting, not each pair.
type MemoryTension struct {
	Kind     MemoryTensionKind `json:"kind"`
	ClaimIDs []string          `json:"claim_ids"`
	Detail   string            `json:"detail"`
}

// NormalizeProposition folds a proposition to a comparable form: case-folded,
// whitespace-collapsed. Exact structural duplicates (the observed loop noise)
// collapse to one group; near-paraphrases deliberately do not — guessing at
// semantic equality would itself be a silent judgment call.
func NormalizeProposition(proposition string) string {
	return strings.ToLower(strings.Join(strings.Fields(proposition), " "))
}

func claimActive(claim Claim) bool {
	return claim.Status == ClaimCandidate || claim.Status == ClaimSupported
}

type tensionGroup struct {
	tension  MemoryTension
	incoming bool
}

// AllTensions inspects the claim base and reports every tension group among
// active claims. Used by the renderer: the packet states, as a fact, which
// parts of memory the model should not trust blindly.
func AllTensions(claims []Claim) []MemoryTension {
	groups := groupTensions(nil, claims)
	result := make([]MemoryTension, 0, len(groups))
	for _, group := range groups {
		result = append(result, group.tension)
	}
	return result
}

// NewTensions reports only the tension groups that involve at least one
// incoming claim: what this emission introduced into memory, not the whole
// history. Used by the step runtime so frame.transitioned carries the memory
// consequences of the step that caused them.
func NewTensions(existing, incoming []Claim) []MemoryTension {
	groups := groupTensions(existing, incoming)
	result := make([]MemoryTension, 0, len(groups))
	for _, group := range groups {
		if group.incoming {
			result = append(result, group.tension)
		}
	}
	return result
}

// groupTensions walks the merged claim set once, grouping duplicates by
// normalized proposition and contradictions by subject+functional predicate.
// A group is flagged incoming when any member comes from the incoming set.
func groupTensions(existing, incoming []Claim) []tensionGroup {
	incomingIDs := make(map[string]struct{}, len(incoming))
	for _, claim := range incoming {
		incomingIDs[claim.ClaimID] = struct{}{}
	}
	active := make([]Claim, 0, len(existing)+len(incoming))
	for _, claim := range existing {
		if claimActive(claim) {
			active = append(active, claim)
		}
	}
	for _, claim := range incoming {
		if claimActive(claim) {
			active = append(active, claim)
		}
	}
	// Duplicates: proposition-normalized grouping.
	type dupGroup struct {
		claims   []Claim
		incoming bool
	}
	duplicates := map[string]*dupGroup{}
	for _, claim := range active {
		key := NormalizeProposition(claim.Proposition)
		group, ok := duplicates[key]
		if !ok {
			group = &dupGroup{}
			duplicates[key] = group
		}
		group.claims = append(group.claims, claim)
		if _, ok := incomingIDs[claim.ClaimID]; ok {
			group.incoming = true
		}
	}
	// Contradictions: functional triples grouped by subject+predicate, then
	// split by object.
	type contraKey struct {
		subject   string
		predicate string
	}
	type contraGroup struct {
		objects  map[string][]Claim
		incoming bool
	}
	contradictions := map[contraKey]*contraGroup{}
	for _, claim := range active {
		if !claim.HasTriple() || !entity.FunctionalPredicate(claim.Predicate) {
			continue
		}
		key := contraKey{subject: claim.Subject, predicate: claim.Predicate}
		group, ok := contradictions[key]
		if !ok {
			group = &contraGroup{objects: map[string][]Claim{}}
			contradictions[key] = group
		}
		group.objects[claim.Object] = append(group.objects[claim.Object], claim)
		if _, ok := incomingIDs[claim.ClaimID]; ok {
			group.incoming = true
		}
	}
	groups := make([]tensionGroup, 0)
	for _, group := range duplicates {
		if len(group.claims) < 2 {
			continue
		}
		ids := make([]string, 0, len(group.claims))
		for _, claim := range group.claims {
			ids = append(ids, claim.ClaimID)
		}
		sort.Strings(ids)
		groups = append(groups, tensionGroup{
			tension: MemoryTension{
				Kind:     TensionDuplicate,
				ClaimIDs: ids,
				Detail:   fmt.Sprintf("proposition %q asserted by %d active claims", NormalizeProposition(group.claims[0].Proposition), len(group.claims)),
			},
			incoming: group.incoming,
		})
	}
	for key, group := range contradictions {
		if len(group.objects) < 2 {
			continue
		}
		objects := make([]string, 0, len(group.objects))
		all := make([]Claim, 0)
		for object := range group.objects {
			objects = append(objects, object)
			all = append(all, group.objects[object]...)
		}
		sort.Strings(objects)
		ids := make([]string, 0, len(all))
		for _, claim := range all {
			ids = append(ids, claim.ClaimID)
		}
		sort.Strings(ids)
		quoted := make([]string, 0, len(objects))
		for _, object := range objects {
			quoted = append(quoted, fmt.Sprintf("%q", object))
		}
		groups = append(groups, tensionGroup{
			tension: MemoryTension{
				Kind:     TensionContradiction,
				ClaimIDs: ids,
				Detail:   fmt.Sprintf("%s %s both %s", key.subject, key.predicate, strings.Join(quoted, " and ")),
			},
			incoming: group.incoming,
		})
	}
	// Deterministic output regardless of map iteration order.
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].tension.Kind != groups[j].tension.Kind {
			return groups[i].tension.Kind < groups[j].tension.Kind
		}
		return groups[i].tension.ClaimIDs[0] < groups[j].tension.ClaimIDs[0]
	})
	return groups
}
