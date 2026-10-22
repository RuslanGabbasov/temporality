package sandbox

import (
	"strings"
	"testing"
)

// The operator recipe (D1) travels verbatim into docker build stdin under
// the custom-profile tag convention.
const validOperatorRecipe = `# operator recipe
FROM python:3.12-alpine AS build
RUN apk add --no-cache gcc musl-dev
FROM python:3.12-alpine
COPY --from=build /usr/local/bin/tool /usr/local/bin/tool
CMD ["tool"]
`

func TestOverrideDefinitionTravelsVerbatim(t *testing.T) {
	def, err := OverrideDefinition("qa-tools", validOperatorRecipe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Dockerfile != validOperatorRecipe {
		t.Errorf("dockerfile must travel verbatim, got:\n%s", def.Dockerfile)
	}
	if def.Tag != "temporality-sandbox-qa-tools:latest" {
		t.Errorf("unexpected tag %q", def.Tag)
	}
	if def.Profile != "qa-tools" {
		t.Errorf("unexpected profile %q", def.Profile)
	}
}

func TestOverrideDefinitionRejectsBadIDAndText(t *testing.T) {
	if _, err := OverrideDefinition("Bad_ID", validOperatorRecipe); err == nil {
		t.Error("expected error for invalid profile id")
	}
	if _, err := OverrideDefinition("qa-tools", "   \n\t"); err == nil {
		t.Error("expected error for an empty override")
	}
	if _, err := OverrideDefinition("qa-tools", "RUN echo hi"); err == nil {
		t.Error("expected error for an override without FROM")
	}
}

// AC-5/D7: every D3/D7 rejection names the offending line and the reason.
func TestValidateDockerfileOverrideLineNumbers(t *testing.T) {
	cases := map[string]struct{ text, wantLine, wantReason string }{
		"add":           {"FROM alpine\nADD http://example.com/x.tar /x", "строка 2", "ADD запрещён"},
		"copy no from":  {"\n# comment\nFROM alpine\nCOPY app /app", "строка 4", "COPY без --from"},
		"onbuild":       {"FROM alpine\nONBUILD COPY app /app", "строка 2", "ONBUILD запрещён"},
		"lowercase add": {"from alpine\nadd x y", "строка 2", "ADD запрещён"},
		"nul":           {"FROM alpine\nRUN x\x00y", "строка 2", "NUL"},
		"invalid utf8":  {"FROM alpine\nRUN echo \xff\xfe", "строка 2", "UTF-8"},
		// D7.1: parser directives switch the build frontend. The ban is
		// stable against BOM/CRLF prefixes and covers any directive-shaped
		// # key= comment before the first instruction (F-01).
		"syntax directive":           {"# syntax=docker/dockerfile:1\nFROM alpine:3.20\n", "строка 1", "#syntax="},
		"escape directive":           {"FROM alpine:3.20\n# escape=`\nRUN echo hi\n", "строка 2", "#escape="},
		"uppercase directive":        {"FROM alpine:3.20\n# Syntax=docker/dockerfile:1.7\n", "строка 2", "#syntax="},
		"bom syntax directive":       {"\ufeff# syntax=docker/dockerfile:1\nFROM alpine:3.20\n", "строка 1", "#syntax="},
		"crlf syntax directive":      {"# syntax=docker/dockerfile:1\r\nFROM alpine:3.20\r\n", "строка 1", "#syntax="},
		"bom crlf escape directive":  {"\ufeff# escape=`\r\nFROM alpine:3.20\r\n", "строка 1", "#escape="},
		"indented bom directive":     {"\ufeff  # escape=`\nFROM alpine:3.20\n", "строка 1", "#escape="},
		"generic directive":          {"# key=value\nFROM alpine:3.20\n", "строка 1", "#key="},
		"generic directive crlf bom": {"\ufeff# note=hello\r\nFROM alpine:3.20\r\n", "строка 1", "#note="},
		// D7.2: RUN --network=host escapes the build network namespace. A
		// token cut by a line continuation is rejoined the way the docker
		// parser glues continuations — byte concatenation, no separator
		// inserted (F-02).
		"run network host":         {"FROM alpine:3.20\nRUN --network=host nmap -sS 10.0.0.0/8\n", "строка 2", "--network=host"},
		"run network host cont":    {"FROM alpine:3.20\nRUN \\\n  --network=host \\\n  echo hi\n", "строка 3", "--network=host"},
		"run network host upper":   {"FROM alpine:3.20\nRUN --network=HOST echo hi\n", "строка 2", "--network=host"},
		"run network ho st":        {"FROM alpine:3.20\nRUN --network=ho\\\nst echo hi\n", "строка 2", "--network=host"},
		"run network eq host":      {"FROM alpine:3.20\nRUN --network=\\\nhost nmap\n", "строка 2", "--network=host"},
		"run net work host":        {"FROM alpine:3.20\nRUN --net\\\nwork=host nmap\n", "строка 2", "--network=host"},
		"run network empty cont":   {"FROM alpine:3.20\nRUN --network=ho\\\n\nst echo hi\n", "строка 2", "--network=host"},
		"run network comment cont": {"FROM alpine:3.20\nRUN --network=ho\\\n# dropped comment\nst echo hi\n", "строка 2", "--network=host"},
		// D7.2/F-03: --security=insecure/privileged drops the daemon's
		// seccomp/AppArmor confinement for the build step.
		"run security insecure":    {"FROM alpine:3.20\nRUN --security=insecure nmap\n", "строка 2", "--security=insecure"},
		"run security privileged":  {"FROM alpine:3.20\nRUN --security=privileged echo hi\n", "строка 2", "--security=privileged"},
		"run security insecure up": {"FROM alpine:3.20\nRUN --security=INSECURE echo hi\n", "строка 2", "--security=insecure"},
		"run security cont":        {"FROM alpine:3.20\nRUN --security=in\\\nsecure echo hi\n", "строка 2", "--security=insecure"},
		// D7.2/F-04: --mount is denied except type=cache.
		"run mount bind":   {"FROM alpine:3.20\nRUN --mount=type=bind,from=base /a /b\n", "строка 2", "--mount"},
		"run mount ssh":    {"FROM alpine:3.20\nRUN --mount=type=ssh git push\n", "строка 2", "--mount"},
		"run mount secret": {"FROM alpine:3.20\nRUN --mount=type=secret,id=token cmd\n", "строка 2", "--mount"},
		"run mount tmpfs":  {"FROM alpine:3.20\nRUN --mount=type=tmpfs,target=/x cmd\n", "строка 2", "--mount"},
		"run mount bare":   {"FROM alpine:3.20\nRUN --mount /a /b\n", "строка 2", "--mount"},
		"run mount cont":   {"FROM alpine:3.20\nRUN --mount=type=\\\nbind /a /b\n", "строка 2", "--mount"},
		// The allowlist is exactly type=cache — a near-miss spelling
		// (type=cache-allowlist) is not the cache mount and stays denied.
		"run mount cache allowlist": {"FROM alpine:3.20\nRUN --mount=type=cache-allowlist,target=/x cmd\n", "строка 2", "--mount"},
		// D7.2/F-04a: the --mount option set must be exactly one cache type
		// spelled once — buildkit resolves duplicated keys by overwrite, so
		// a repeated key or a non-cache type would flip the effective mount.
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
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateDockerfileOverride(tc.text)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantLine) || !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("error must name %q and %q, got: %v", tc.wantLine, tc.wantReason, err)
			}
		})
	}

	// No FROM anywhere in the file.
	err := ValidateDockerfileOverride("RUN echo hi\n# still no FROM\n")
	if err == nil || !strings.Contains(err.Error(), "FROM") {
		t.Fatalf("expected a missing-FROM error, got: %v", err)
	}

	// Size overflow points at the line that crosses the 16 KiB budget.
	oversized := "FROM alpine:3.20\n" + strings.Repeat("RUN echo "+strings.Repeat("x", 100)+"\n", 165)
	err = ValidateDockerfileOverride(oversized)
	if err == nil || !strings.Contains(err.Error(), "строка") || !strings.Contains(err.Error(), "16 KiB") {
		t.Fatalf("expected a size error with a line, got: %v", err)
	}
}

func TestValidateDockerfileOverrideAccepts(t *testing.T) {
	for name, text := range map[string]string{
		"plain":      "FROM alpine:3.20\nRUN apk add --no-cache git\n",
		"multistage": validOperatorRecipe,
		"lowercase":  "from alpine:3.20 as builder\nrun apk add git\nfrom alpine:3.20\ncopy --from=builder /a /a\n",
		"crlf":       "FROM alpine\r\nRUN apk add git\r\n",
		"json copy":  "FROM x AS b\nCOPY --from=b [\"a\", \"b\"]\n",
		"comments":   "# a comment mentioning ADD COPY ONBUILD\n# RUN --network=host stays a comment, not an instruction\nFROM alpine\n",
		// D7.2 scope: only RUN is checked — a plain command mentioning the
		// flag value as data still passes.
		"network flag elsewhere": "FROM alpine\nRUN echo docker run --network=host\n",
		// The --mount allowlist: type=cache stays the one permitted mount.
		"cache mount": "FROM alpine:3.20\nRUN --mount=type=cache,target=/root/.cache pip install -r reqs.txt\n",
		// F-04a: the one cache type spelled once — plain, with the whole
		// value quoted, with a quoted type value, and with case-folded keys
		// plus well-formed cache options.
		"cache mount one target":   "FROM alpine:3.20\nRUN --mount=type=cache,target=/x cmd\n",
		"cache mount quoted value": "FROM alpine:3.20\nRUN --mount=\"type=cache\",target=/x cmd\n",
		"cache mount quoted type":  "FROM alpine:3.20\nRUN --mount=type=\"cache\" cmd\n",
		"cache mount folded":       "FROM alpine:3.20\nRUN --mount=type=Cache,ID=ok-1.2_3,sharing=private,mode=0755,uid=65534,gid=65534 cmd\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateDockerfileOverride(text); err != nil {
				t.Fatalf("valid recipe rejected: %v", err)
			}
		})
	}
}

// F-04a: every --mount rejection names the offending option and keeps the
// canonical invariant phrase — an operator must see WHY the option set is
// not the one allowed cache mount.
func TestValidateDockerfileOverrideMountRejectionReasons(t *testing.T) {
	for name, tc := range map[string]struct{ text, wantReason string }{
		"duplicate type":   {"FROM alpine:3.20\nRUN --mount=type=cache,type=bind cmd\n", `дубль ключа опции "type"`},
		"duplicate target": {"FROM alpine:3.20\nRUN --mount=type=cache,target=/x,target=/y cmd\n", `дубль ключа опции "target"`},
		"non-cache type":   {"FROM alpine:3.20\nRUN --mount=type=bind cmd\n", `тип "bind" вместо cache`},
		"unknown key":      {"FROM alpine:3.20\nRUN --mount=type=cache,from=base cmd\n", "не входит в список разрешённых"},
		"bad sharing":      {"FROM alpine:3.20\nRUN --mount=type=cache,sharing=evil cmd\n", "shared, private, locked"},
		"path id":          {"FROM alpine:3.20\nRUN --mount=type=cache,id=../x cmd\n", "[A-Za-z0-9_.-]{1,64}"},
		"empty value":      {"FROM alpine:3.20\nRUN --mount= cmd\n", "значение флага отсутствует или пустое"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateDockerfileOverride(tc.text)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantReason) ||
				!strings.Contains(err.Error(), "допустим только один type=cache без дублей ключей") ||
				!strings.Contains(err.Error(), "строка 2") {
				t.Fatalf("error must explain the rejection with the invariant phrase, got: %v", err)
			}
		})
	}
}

func TestFirstFromImage(t *testing.T) {
	if got := firstFromImage(validOperatorRecipe); got != "python:3.12-alpine" {
		t.Errorf("firstFromImage = %q", got)
	}
	if got := firstFromImage("# only comments\nRUN echo hi\n"); got != "" {
		t.Errorf("firstFromImage without FROM = %q, want empty", got)
	}
}
