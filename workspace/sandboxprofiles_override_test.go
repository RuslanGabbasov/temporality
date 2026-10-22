package workspace

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	kernelsandbox "github.com/temporality-project/temporality/kernel/sandbox"
)

const operatorRecipeFixture = "FROM python:3.12-alpine AS build\nRUN apk add --no-cache gcc\nFROM python:3.12-alpine\nCOPY --from=build /a /a\n"

// AC-2/D2.1: exactly one recipe source — mixing the operator override with
// base/packages or a direct image is an explicit 422-class error.
func TestValidateSandboxProfileRecipeSourceXOR(t *testing.T) {
	override := SandboxProfile{ID: "qa-tools", Name: "QA", DockerfileOverride: operatorRecipeFixture}
	require.NoError(t, ValidateSandboxProfile(override))

	baseMix := override
	baseMix.Base = "python"
	err := ValidateSandboxProfile(baseMix)
	require.True(t, errors.Is(err, ErrProfileUnprocessable), "base+override must be unprocessable, got: %v", err)
	require.Contains(t, err.Error(), "dockerfile_override excludes base and packages")

	packagesMix := override
	packagesMix.Packages = []string{"gcc"}
	err = ValidateSandboxProfile(packagesMix)
	require.True(t, errors.Is(err, ErrProfileUnprocessable), "packages+override must be unprocessable, got: %v", err)

	imageMix := override
	imageMix.Image = "repo@sha256:" + strings.Repeat("ab", 32)
	err = ValidateSandboxProfile(imageMix)
	require.True(t, errors.Is(err, ErrProfileUnprocessable), "image+override must be unprocessable, got: %v", err)
	require.Contains(t, err.Error(), "excludes a direct image")
}

// AC-3/D2.4: builtins ship with kernel updates and never take an override.
func TestValidateSandboxProfileBuiltinRejectsOverride(t *testing.T) {
	p := SandboxProfile{ID: "node", Name: "Node 22", Builtin: true, DockerfileOverride: "FROM alpine:3.20\n"}
	err := ValidateSandboxProfile(p)
	require.True(t, errors.Is(err, ErrProfileUnprocessable), "builtin+override must be unprocessable, got: %v", err)
}

// The workspace mirror of D3 rejects bad texts at save time with a
// line-numbered error.
func TestValidateSandboxProfileOverrideTextMirror(t *testing.T) {
	p := SandboxProfile{ID: "op-recipe", Name: "OP", DockerfileOverride: "FROM alpine:3.20\nADD x y\n"}
	err := ValidateSandboxProfile(p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dockerfile_override")
	require.Contains(t, err.Error(), "строка 2")

	blank := SandboxProfile{ID: "op-recipe", Name: "OP", DockerfileOverride: "   "}
	require.Error(t, ValidateSandboxProfile(blank))
}

// D7 rules mirrored at save time: parser directives and RUN --network=host
// fail the PUT with a line-numbered error (API 400 path), exactly like the
// kernel/sandbox authority fails the build.
func TestValidSandboxDockerfileRejectsD7Rules(t *testing.T) {
	for name, tc := range map[string]struct{ text, wantLine, wantReason string }{
		"syntax directive":        {"# syntax=docker/dockerfile:1\nFROM alpine:3.20\n", "строка 1", "#syntax="},
		"bom syntax directive":    {"\ufeff# syntax=docker/dockerfile:1\nFROM alpine:3.20\n", "строка 1", "#syntax="},
		"escape directive":        {"FROM alpine:3.20\n# escape=`\nRUN echo hi\n", "строка 2", "#escape="},
		"generic directive":       {"# key=value\nFROM alpine:3.20\n", "строка 1", "#key="},
		"run network host":        {"FROM alpine:3.20\nRUN --network=host nmap -sS 10.0.0.0/8\n", "строка 2", "--network=host"},
		"run network ho st":       {"FROM alpine:3.20\nRUN --network=ho\\\nst echo hi\n", "строка 2", "--network=host"},
		"run network host quoted": {"FROM alpine:3.20\nRUN --network=\"host\" echo hi\n", "строка 2", "--network=host"},
		"run security insecure":   {"FROM alpine:3.20\nRUN --security=insecure nmap\n", "строка 2", "--security=insecure"},
		"run mount cache allowlist": {"FROM alpine:3.20\nRUN --mount=type=cache-allowlist,target=/x cmd\n", "строка 2", "--mount"},
		// F-04a mirror: the --mount option set is one cache type spelled
		// once — duplicated keys and non-cache types are rejected at save
		// time exactly like the kernel authority rejects them at build.
		"run mount dup type cache bind":   {"FROM alpine:3.20\nRUN --mount=type=cache,type=bind,from=base,target=/x cmd\n", "строка 2", "--mount"},
		"run mount dup type bind cache":   {"FROM alpine:3.20\nRUN --mount=type=bind,type=cache cmd\n", "строка 2", "--mount"},
		"run mount dup type cache secret": {"FROM alpine:3.20\nRUN --mount=type=cache,type=secret cmd\n", "строка 2", "--mount"},
		"run mount dup type quoted bind":  {"FROM alpine:3.20\nRUN --mount=type=cache,type=\"bind\" cmd\n", "строка 2", "--mount"},
		"run mount dup target":            {"FROM alpine:3.20\nRUN --mount=type=cache,target=/x,target=/y cmd\n", "строка 2", "--mount"},
		"run mount bad sharing":           {"FROM alpine:3.20\nRUN --mount=type=cache,sharing=evil cmd\n", "строка 2", "--mount"},
		"run mount path id":               {"FROM alpine:3.20\nRUN --mount=type=cache,id=../x cmd\n", "строка 2", "--mount"},
		"run mount unknown option":        {"FROM alpine:3.20\nRUN --mount=type=cache,from=base cmd\n", "строка 2", "--mount"},
		"run mount empty value":           {"FROM alpine:3.20\nRUN --mount= cmd\n", "строка 2", "--mount"},
		"run mount non digit mode":        {"FROM alpine:3.20\nRUN --mount=type=cache,mode=07x5 cmd\n", "строка 2", "--mount"},
		"run mount without type":          {"FROM alpine:3.20\nRUN --mount=target=/x cmd\n", "строка 2", "--mount"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidSandboxDockerfile(tc.text)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantLine)
			require.Contains(t, err.Error(), tc.wantReason)
		})
	}
	// Content that merely mentions the flag is not a flag.
	require.NoError(t, ValidSandboxDockerfile("FROM alpine:3.20\nRUN echo --network=host\n"))
	// F-04a: the one cache type spelled once — plain, quoted value, quoted
	// type value, case-folded keys with well-formed cache options.
	for _, text := range []string{
		"FROM alpine:3.20\nRUN --mount=type=cache,target=/x cmd\n",
		"FROM alpine:3.20\nRUN --mount=\"type=cache\",target=/x cmd\n",
		"FROM alpine:3.20\nRUN --mount=type=\"cache\" cmd\n",
		"FROM alpine:3.20\nRUN --mount=type=Cache,ID=ok-1.2_3,sharing=private,mode=0755,uid=65534,gid=65534 cmd\n",
	} {
		require.NoError(t, ValidSandboxDockerfile(text))
	}
}

// AC-4/D2.3: any recipe edit — including the override text — resets the
// pinned image; the stale digest must not survive a recipe change.
func TestPrepareSandboxProfileUpdateResetsImageOnOverrideEdit(t *testing.T) {
	digest := "repo@sha256:" + strings.Repeat("ab", 32)
	existing := SandboxProfile{ID: "op-recipe", Name: "OP", DockerfileOverride: "FROM alpine:3.20\n", Image: digest}

	edited := prepareSandboxProfileUpdate(existing, SandboxProfile{ID: "op-recipe", Name: "OP", DockerfileOverride: "FROM alpine:3.21\n"})
	require.Empty(t, edited.Image, "override edit must reset the pinned image")
	require.Equal(t, "FROM alpine:3.21\n", edited.DockerfileOverride)

	// An echoed image rides the same reset when the text changed.
	echo := prepareSandboxProfileUpdate(existing, SandboxProfile{ID: "op-recipe", Name: "OP", DockerfileOverride: "FROM alpine:3.21\n", Image: digest})
	require.Empty(t, echo.Image)

	// Switching away from the override clears the stale digest too.
	toBase := prepareSandboxProfileUpdate(existing, SandboxProfile{ID: "op-recipe", Name: "OP", Base: "python"})
	require.Empty(t, toBase.Image)
	require.Equal(t, "python", toBase.Base)

	// A builtin PUT keeps catalog identity and leaves the incoming override
	// in place so validation rejects it explicitly (no silent drop).
	builtin := SandboxProfile{ID: "node", Name: "Rename", DockerfileOverride: "FROM alpine:3.20\n", Memory: "3g"}
	merged := prepareSandboxProfileUpdate(SandboxProfile{ID: "node", Name: "Node 22", Builtin: true}, builtin)
	require.Equal(t, "Node 22", merged.Name)
	require.True(t, merged.Builtin)
	require.Equal(t, "FROM alpine:3.20\n", merged.DockerfileOverride)
}

// The composed base recipe keeps its reset behavior (regression guard).
func TestPrepareSandboxProfileUpdateResetsImageOnBaseEdit(t *testing.T) {
	digest := "repo@sha256:" + strings.Repeat("ab", 32)
	existing := SandboxProfile{ID: "qa-tools", Name: "QA", Base: "python", Packages: []string{"gcc"}, Image: digest}
	edited := prepareSandboxProfileUpdate(existing, SandboxProfile{ID: "qa-tools", Name: "QA", Base: "python", Packages: []string{"gcc", "linux-headers"}})
	require.Empty(t, edited.Image, "base recipe edit must reset the pinned image")
}

// AC-10: the field serializes under dockerfile_override and stays out of the
// JSON when empty (omitempty).
func TestSandboxProfileDockerfileOverrideJSON(t *testing.T) {
	raw, err := json.Marshal(SandboxProfile{ID: "op-recipe", Name: "OP"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "dockerfile_override")

	raw, err = json.Marshal(SandboxProfile{ID: "op-recipe", Name: "OP", DockerfileOverride: "FROM alpine:3.20\n"})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"dockerfile_override":"FROM alpine:3.20\n"`)
}

// mountOptionCases is the F-04a/F-04b probe corpus with expected verdicts:
// buildkit resolves duplicated option keys by overwrite, so "any type=cache"
// is an insufficient scan — the option set must be exactly one cache type
// spelled once, with allowlisted well-formed options. The texts feed the
// parity corpus below and TestSandboxDockerfileMountOptionRules.
var mountOptionCases = []struct {
	text string
	ok   bool
}{
	{"FROM alpine:3.20\nRUN --mount=type=cache,type=bind,from=base,target=/x cmd\n", false},
	{"FROM alpine:3.20\nRUN --mount=type=bind,type=cache cmd\n", false},
	{"FROM alpine:3.20\nRUN --mount=type=cache,type=secret cmd\n", false},
	{"FROM alpine:3.20\nRUN --mount=type=cache,target=/x,target=/y cmd\n", false},
	{"FROM alpine:3.20\nRUN --mount=type=cache,sharing=evil cmd\n", false},
	{"FROM alpine:3.20\nRUN --mount=type=cache,id=../x cmd\n", false},
	{"FROM alpine:3.20\nRUN --mount=type=cache,type=\"bind\" cmd\n", false},
	{"FROM alpine:3.20\nRUN --mount=type=cache,target=/x cmd\n", true},
	{"FROM alpine:3.20\nRUN --mount=\"type=cache\",target=/x cmd\n", true},
	{"FROM alpine:3.20\nRUN --mount=type=\"cache\" cmd\n", true},
	{"FROM alpine:3.20\nRUN --mount=type=Cache,ID=ok-1.2_3,sharing=private,mode=0755,uid=65534,gid=65534 cmd\n", true},
}

// The workspace mirror and the kernel/sandbox authority must accept and
// reject exactly the same texts — they guard one rule on two sides of the
// API boundary.
func TestSandboxDockerfileValidatorsAgree(t *testing.T) {
	oversized := "FROM alpine:3.20\n" + strings.Repeat("RUN echo "+strings.Repeat("x", 100)+"\n", 165)
	corpus := []string{
		"FROM alpine:3.20\nRUN apk add --no-cache git\n",
		operatorRecipeFixture,
		"from alpine:3.20 as b\nfrom alpine:3.20\ncopy --from=b /a /a\n",
		"# syntax=docker/dockerfile:1\nFROM alpine:3.20\n",
		"\ufeff# key=value\nFROM alpine:3.20\n",
		"FROM alpine:3.20\n# escape=`\nRUN echo hi\n",
		"FROM alpine:3.20\nRUN --network=host nmap\n",
		"FROM alpine:3.20\nRUN --network=ho\\\nst echo hi\n",
		"FROM alpine:3.20\nRUN --security=insecure nmap\n",
		"FROM alpine:3.20\nRUN --mount=type=cache-allowlist,target=/x cmd\n",
		"FROM alpine:3.20\nRUN --mount=type=cache,target=/x cmd\n",
		"FROM alpine:3.20\nRUN echo --network=host\n",
		"",
		"   \n\t",
		"RUN echo hi",
		"FROM alpine:3.20\nADD x y\n",
		"FROM alpine:3.20\nCOPY app /app\n",
		"FROM alpine:3.20\nONBUILD RUN x\n",
		"FROM alpine:3.20\nRUN x\x00y",
		"FROM alpine:3.20\nRUN \xff\xfe",
		oversized,
	}
	for _, tc := range mountOptionCases {
		corpus = append(corpus, tc.text)
	}
	for _, text := range corpus {
		mirror := ValidSandboxDockerfile(text)
		_, authority := kernelsandbox.OverrideDefinition("probe", text)
		require.Equal(t, mirror == nil, authority == nil,
			"validators disagree on %q: mirror=%v authority=%v", text, mirror, authority)
	}
}

// F-04a/F-04b: both validators parse the --mount option set strictly — the
// same expected verdict on each side of the API boundary, and a rejection
// that keeps the canonical invariant phrase.
func TestSandboxDockerfileMountOptionRules(t *testing.T) {
	for _, tc := range mountOptionCases {
		mirror := ValidSandboxDockerfile(tc.text)
		_, authority := kernelsandbox.OverrideDefinition("probe", tc.text)
		require.Equal(t, tc.ok, mirror == nil, "mirror verdict on %q: %v", tc.text, mirror)
		require.Equal(t, tc.ok, authority == nil, "authority verdict on %q: %v", tc.text, authority)
		if !tc.ok {
			require.Contains(t, mirror.Error(), "допустим только один type=cache без дублей ключей")
			require.Contains(t, authority.Error(), "допустим только один type=cache без дублей ключей")
		}
	}
}
