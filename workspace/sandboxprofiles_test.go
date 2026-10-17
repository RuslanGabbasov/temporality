package workspace

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinSandboxProfilesCatalogSanity(t *testing.T) {
	profiles := BuiltinSandboxProfiles()
	ids := map[string]bool{}
	for _, p := range profiles {
		require.False(t, ids[p.ID], "duplicate builtin id %s", p.ID)
		ids[p.ID] = true
		require.True(t, p.Builtin)
		require.NoError(t, ValidateSandboxProfile(p))
		require.Empty(t, p.Image, "builtin images are pinned by build, not by catalog")
		require.NotEmpty(t, p.Toolchains)
	}
	require.Contains(t, ids, "node")
	require.Contains(t, ids, "python")
	require.Contains(t, ids, "browser-e2e")
}

// The browser profile carries heavier resources than the installation
// default — that is its reason to exist beyond the toolchain.
func TestBrowserProfileRaisesResources(t *testing.T) {
	var browser SandboxProfile
	for _, p := range BuiltinSandboxProfiles() {
		if p.ID == "browser-e2e" {
			browser = p
		}
	}
	require.Equal(t, "2g", browser.Memory)
	require.Equal(t, "1g", browser.ScratchSize)
	require.Contains(t, browser.CacheEnv, "PLAYWRIGHT_BROWSERS_PATH=/cache/ms-playwright")
}

func TestCacheSubdirs(t *testing.T) {
	dirs := CacheSubdirs([]string{
		"NPM_CONFIG_CACHE=/cache/npm",
		"PIP_CACHE_DIR=/cache/pip",
		"PIP_CACHE_DIR=/cache/pip", // duplicate collapses
		"PLAYWRIGHT_BROWSERS_PATH=/cache/ms-playwright",
		"GOCACHE=/cache/go-build",
		"OTHER=/scratch/x",  // not a cache dir — skipped
		"SOMETHING=/cache/", // bare root — skipped
		"SOMETHING2=/cache", // bare root — skipped
	})
	require.Equal(t, []string{"npm", "pip", "ms-playwright", "go-build"}, dirs)
}

func TestValidateSandboxProfile(t *testing.T) {
	valid := SandboxProfile{ID: "node-custom", Name: "Node custom", Image: "repo.io/sandbox@sha256:" + strings.Repeat("ab", 32), CacheEnv: []string{"NPM_CONFIG_CACHE=/cache/npm"}}
	require.NoError(t, ValidateSandboxProfile(valid))

	cases := map[string]func(p *SandboxProfile){
		"bad id":          func(p *SandboxProfile) { p.ID = "Node!" },
		"id too long":     func(p *SandboxProfile) { p.ID = "a" + strings.Repeat("b", 40) },
		"empty name":      func(p *SandboxProfile) { p.Name = "  " },
		"tag image":       func(p *SandboxProfile) { p.Image = "node:22-alpine" },
		"short digest":    func(p *SandboxProfile) { p.Image = "repo@sha256:abc" },
		"non-hex digest":  func(p *SandboxProfile) { p.Image = "repo@sha256:" + strings.Repeat("zz", 32) },
		"bad memory":      func(p *SandboxProfile) { p.Memory = "lots" },
		"bad cpus":        func(p *SandboxProfile) { p.CPUs = "two" },
		"cache no value":  func(p *SandboxProfile) { p.CacheEnv = []string{"NPM_CONFIG_CACHE"} },
		"cache outside":   func(p *SandboxProfile) { p.CacheEnv = []string{"X=/tmp/cache"} },
		"cache bare root": func(p *SandboxProfile) { p.CacheEnv = []string{"X=/cache/"} },
		"image with base": func(p *SandboxProfile) {
			p.Image = ""
			p.Base = "python"
			p.Image = "repo@sha256:" + strings.Repeat("ab", 32)
		},
		"unknown base":     func(p *SandboxProfile) { p.Base = "invented" },
		"packages no base": func(p *SandboxProfile) { p.Packages = []string{"gcc"} },
		"bad package":      func(p *SandboxProfile) { p.Base = "python"; p.Packages = []string{"gcc; rm"} },
		"builtin base":     func(p *SandboxProfile) { p.Base = "python"; p.Builtin = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := valid
			mutate(&p)
			require.Error(t, ValidateSandboxProfile(p))
		})
	}

	// Recipe-based custom profile: no image yet, base from the known set.
	recipe := SandboxProfile{ID: "qa-tools", Name: "QA tools", Base: "python", Packages: []string{"gcc", "linux-headers"}}
	require.NoError(t, ValidateSandboxProfile(recipe))
}

func TestIsDigestPinnedImage(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	require.True(t, IsDigestPinnedImage("temporality-sandbox@sha256:"+digest))
	require.True(t, IsDigestPinnedImage("repo:443/img@sha256:"+digest))
	require.False(t, IsDigestPinnedImage("img:latest"))
	require.False(t, IsDigestPinnedImage("@sha256:"+digest))
	require.False(t, IsDigestPinnedImage("img@sha256:short"))
}
