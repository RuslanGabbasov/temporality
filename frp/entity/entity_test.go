package entity

import (
	"regexp"
	"testing"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		raw      string
		wantType string
		wantName string
		wantErr  bool
	}{
		{raw: "module:github.com/example/demo", wantType: "module", wantName: "github.com/example/demo"},
		{raw: "endpoint:https://api.example.com:8080/v1", wantType: "endpoint", wantName: "https://api.example.com:8080/v1"},
		{raw: "file:go.mod", wantType: "file", wantName: "go.mod"},
		{raw: "branch:main", wantType: "branch", wantName: "main"},
		{raw: "nosuchtype:name", wantErr: true},
		{raw: "module:", wantErr: true},
		{raw: "module", wantErr: true},
		{raw: "", wantErr: true},
	}
	for _, tc := range cases {
		ref, err := ParseRef(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRef(%q): expected error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRef(%q): unexpected error %v", tc.raw, err)
			continue
		}
		if ref.Type != tc.wantType || ref.Name != tc.wantName {
			t.Errorf("ParseRef(%q) = %v, want %s:%s", tc.raw, ref, tc.wantType, tc.wantName)
		}
		if ref.String() != tc.raw {
			t.Errorf("String() = %q, want %q", ref.String(), tc.raw)
		}
	}
}

func TestRefNameLengthLimit(t *testing.T) {
	long := make([]byte, 513)
	for i := range long {
		long[i] = 'a'
	}
	ref := Ref{Type: TypeModule, Name: string(long)}
	if err := ref.Validate(); err == nil {
		t.Error("expected length limit error")
	}
}

func TestEntityIDDeterministic(t *testing.T) {
	a := Ref{Type: TypeModule, Name: "github.com/example/demo"}.ID()
	b := Ref{Type: TypeModule, Name: "github.com/example/demo"}.ID()
	if a != b {
		t.Fatalf("entity id not deterministic: %s != %s", a, b)
	}
	other := Ref{Type: TypePackage, Name: "github.com/example/demo"}.ID()
	if a == other {
		t.Fatal("different types must produce different ids")
	}
	otherName := Ref{Type: TypeModule, Name: "other"}.ID()
	if a == otherName {
		t.Fatal("different names must produce different ids")
	}
}

func TestEntityIDShape(t *testing.T) {
	id := Ref{Type: TypeRepository, Name: "repo"}.ID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("entity id %q is not a v5-shaped UUID", id)
	}
}

func TestRegistries(t *testing.T) {
	types := CanonicalEntityTypes()
	if len(types) == 0 || types[0] != types[len(types)-1] && types[0] > types[len(types)-1] {
		t.Fatal("canonical entity types not sorted")
	}
	for _, name := range types {
		if err := ValidateType(name); err != nil {
			t.Errorf("ValidateType(%q): %v", name, err)
		}
	}
	preds := CanonicalPredicates()
	for _, name := range preds {
		if err := ValidatePredicate(name); err != nil {
			t.Errorf("ValidatePredicate(%q): %v", name, err)
		}
	}
	if err := ValidateType("bogus"); err == nil {
		t.Error("ValidateType must reject unknown types")
	}
	if err := ValidatePredicate("bogus"); err == nil {
		t.Error("ValidatePredicate must reject unknown predicates")
	}
	// Functional predicates gate M16 contradiction detection: they must be a
	// subset of the registry, and the set must stay conservative — flagging a
	// one-to-many relation as functional would report false contradictions.
	functional := map[string]bool{}
	for _, name := range preds {
		functional[name] = FunctionalPredicate(name)
		if functional[name] && ValidatePredicate(name) != nil {
			t.Errorf("functional predicate %q not registered", name)
		}
	}
	for _, name := range []string{PredicateDeclaredIn, PredicateOnBranch, PredicateAuthoredBy} {
		if !functional[name] {
			t.Errorf("predicate %q must be functional", name)
		}
	}
	for _, name := range []string{PredicateContains, PredicateDependsOn, PredicateUses, PredicateIntegrates, PredicateTargets, PredicateDocuments, PredicateServes} {
		if functional[name] {
			t.Errorf("predicate %q must not be functional", name)
		}
	}
}
