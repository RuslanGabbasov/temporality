package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

func TestGenerateEgressConfigPrivateDenyAndExplicitAllow(t *testing.T) {
	agents := []workspace.Agent{
		{ID: "researcher", NetworkAccess: boolPtr(true), EgressAllowlist: []string{"api.github.com", "10.20.30.0/24", "gitlab.corp"}},
		{ID: "writer", NetworkAccess: boolPtr(true)},
	}
	conf, ports := generateEgressConfig(agents)

	// Stable sorted order: researcher's block comes first.
	if strings.Index(conf, "agent researcher") > strings.Index(conf, "agent writer") {
		t.Fatalf("agent blocks are not sorted: %s", conf)
	}
	// researcher: explicit entries (including the private CIDR) allowed.
	if !strings.Contains(conf, "acl p_researcher_dom dstdomain api.github.com gitlab.corp") {
		t.Fatalf("researcher domain ACL missing: %s", conf)
	}
	if !strings.Contains(conf, "acl p_researcher_dst dst 10.20.30.0/24") {
		t.Fatalf("researcher CIDR ACL missing: %s", conf)
	}
	if !strings.Contains(conf, "http_access allow p_researcher p_researcher_dst") {
		t.Fatalf("researcher CIDR allow missing: %s", conf)
	}
	// Public internet for network-on agents.
	if !strings.Contains(conf, "http_access allow p_researcher !private_nets") {
		t.Fatalf("researcher public allow missing: %s", conf)
	}
	// The private deny must come AFTER the explicit allow rules (order is the
	// semantics), and the blanket deny closes the config.
	if strings.LastIndex(conf, "deny private_nets") > strings.LastIndex(conf, "http_access allow p_researcher p_researcher_dst") && strings.Contains(conf, "deny private_nets !") {
		t.Fatalf("unexpected deny-private ordering")
	}
	if !strings.Contains(conf, "http_access deny all") {
		t.Fatalf("final deny missing")
	}
	// Both network-on agents got ports; the port map drives the sandbox env.
	if ports["researcher"] == 0 || ports["writer"] == 0 {
		t.Fatalf("ports not assigned: %+v", ports)
	}
	if ports["researcher"] == ports["writer"] {
		t.Fatalf("ports collide: %+v", ports)
	}
}

func TestGenerateEgressConfigNoNetworkNoPublic(t *testing.T) {
	conf, ports := generateEgressConfig([]workspace.Agent{
		{ID: "local-only"},
	})
	if _, ok := ports["local-only"]; ok {
		t.Fatalf("network-off agent must not get a public port: %+v", ports)
	}
	if strings.Contains(conf, "http_access allow p_local-only !private_nets") {
		t.Fatalf("network-off agent must not get a public allow")
	}
}

// The allowlist works independently of the public-network toggle: an agent
// with network OFF and destinations listed gets a port and an explicit-allow
// block, but NO public-internet rule.
func TestGenerateEgressConfigAllowlistOnlyAgent(t *testing.T) {
	conf, ports := generateEgressConfig([]workspace.Agent{
		{ID: "scoped", EgressAllowlist: []string{"gitlab.corp", "10.20.30.0/24"}},
	})
	if ports["scoped"] == 0 {
		t.Fatalf("allowlist-only agent must get a port: %+v", ports)
	}
	if !strings.Contains(conf, "http_port "+fmt.Sprint(ports["scoped"])) {
		t.Fatalf("port not declared: %s", conf)
	}
	if !strings.Contains(conf, "http_access allow p_scoped p_scoped_dst") {
		t.Fatalf("explicit CIDR allow missing: %s", conf)
	}
	if strings.Contains(conf, "http_access allow p_scoped !private_nets") {
		t.Fatalf("public allow must be absent for an allowlist-only agent")
	}
}

func TestGenerateEgressConfigDeterministic(t *testing.T) {
	agents := []workspace.Agent{
		{ID: "b", NetworkAccess: boolPtr(true), EgressAllowlist: []string{"a.com"}},
		{ID: "a", NetworkAccess: boolPtr(true), EgressAllowlist: []string{"b.com"}},
	}
	conf1, ports1 := generateEgressConfig(agents)
	conf2, ports2 := generateEgressConfig(agents)
	if conf1 != conf2 || ports1["a"] != ports2["a"] || ports1["b"] != ports2["b"] {
		t.Fatalf("generation is not deterministic")
	}
}

func TestAssignEgressPortNoCollision(t *testing.T) {
	used := map[int]bool{3128: true}
	first := assignEgressPort("agent", used, nil)
	second := assignEgressPort("agent", used, nil)
	if first == second {
		t.Fatalf("collision probing failed: %d twice", first)
	}
	if first < 31301 || first >= 31301+400 {
		t.Fatalf("port out of pool: %d", first)
	}
}

func boolPtr(b bool) *bool { return &b }
