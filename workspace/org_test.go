package workspace

import (
	"reflect"
	"testing"
)

func TestSelfPath(t *testing.T) {
	cases := []struct {
		unit OrgUnit
		want string
	}{
		{OrgUnit{ID: "acme"}, "acme"},
		{OrgUnit{ID: "dev", Path: "acme"}, "acme.dev"},
		{OrgUnit{ID: "platform", Path: "acme.dev"}, "acme.dev.platform"},
	}
	for _, tc := range cases {
		if got := tc.unit.SelfPath(); got != tc.want {
			t.Errorf("SelfPath(%s/%s) = %q, want %q", tc.unit.Path, tc.unit.ID, got, tc.want)
		}
	}
}

func TestAncestors(t *testing.T) {
	cases := []struct {
		unit OrgUnit
		want []string
	}{
		{OrgUnit{ID: "acme"}, []string{"acme"}},
		{OrgUnit{ID: "dev", Path: "acme"}, []string{"acme", "dev"}},
		{OrgUnit{ID: "team", Path: "acme.dev"}, []string{"acme", "dev", "team"}},
	}
	for _, tc := range cases {
		got := tc.unit.Ancestors()
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Ancestors(%s/%s) = %v, want %v", tc.unit.Path, tc.unit.ID, got, tc.want)
		}
	}
}

func TestJoinPath(t *testing.T) {
	if got := joinPath("", "a"); got != "a" {
		t.Errorf(`joinPath("", "a") = %q, want "a"`, got)
	}
	if got := joinPath("a.b", "c"); got != "a.b.c" {
		t.Errorf(`joinPath("a.b", "c") = %q, want "a.b.c"`, got)
	}
}

func TestOrgVisible(t *testing.T) {
	// A viewer in acme.dev.platform sees: own unit, ancestors, and org-neutral
	// resources. Nothing bound to sibling or unrelated units.
	chain := []string{"acme", "acme.dev", "acme.dev.platform"}
	cases := []struct {
		name    string
		bound   string
		units   []string
		visible bool
	}{
		{"global resource", "", chain, true},
		{"own unit", "acme.dev.platform", chain, true},
		{"ancestor", "acme.dev", chain, true},
		{"root", "acme", chain, true},
		{"sibling", "acme.dev.product", chain, false},
		{"other company", "otherco", chain, false},
		{"unfiltered viewer", "acme.dev.product", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OrgVisible(tc.bound, tc.units); got != tc.visible {
				t.Errorf("OrgVisible(%q, %v) = %v, want %v", tc.bound, tc.units, got, tc.visible)
			}
		})
	}
}

func TestOrgAllowsAny(t *testing.T) {
	chain := []string{"acme", "acme.dev"}
	cases := []struct {
		name    string
		bound   []string
		units   []string
		visible bool
	}{
		{"no bindings is org-neutral", nil, chain, true},
		{"binding on ancestor", []string{"acme"}, chain, true},
		{"binding on own unit", []string{"acme.dev"}, chain, true},
		{"one of several bindings matches", []string{"otherco", "acme"}, chain, true},
		{"all bindings outside", []string{"otherco", "otherco.qa"}, chain, false},
		{"binding under viewer is not visible", []string{"acme.dev.platform"}, chain, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orgAllowsAny(tc.bound, tc.units); got != tc.visible {
				t.Errorf("orgAllowsAny(%v, %v) = %v, want %v", tc.bound, tc.units, got, tc.visible)
			}
		})
	}
}

func TestProjectVisibleFor(t *testing.T) {
	// docs/org-structure.md §15-16: access = org-unit access OR explicit
	// membership. Nil units is the transition mode (admins, unassigned
	// viewers) where the org check is skipped entirely.
	chain := []string{"acme", "acme.dev"}
	cases := []struct {
		name     string
		bound    []string
		units    []string
		isMember bool
		visible  bool
	}{
		{"nil units disables filtering", []string{"otherco"}, nil, false, true},
		{"org-neutral project", nil, chain, false, true},
		{"org overlap", []string{"acme"}, chain, false, true},
		{"membership rescues foreign project", []string{"otherco"}, chain, true, true},
		{"no org overlap, not a member", []string{"otherco"}, chain, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectVisibleFor(tc.bound, tc.units, tc.isMember); got != tc.visible {
				t.Errorf("projectVisibleFor(%v, %v, member=%v) = %v, want %v", tc.bound, tc.units, tc.isMember, got, tc.visible)
			}
		})
	}
}
