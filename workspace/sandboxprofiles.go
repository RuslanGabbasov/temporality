package workspace

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Sandbox profile registry (docs/sandbox-profiles.md). A profile is the unit
// of "what toolchain does this agent's sandbox run": an image reference plus
// resource overrides and cache env pointers. Isolation flags never vary by
// profile — the container hardening is identical for everyone.

// SandboxProfile is one registry row. Builtin rows are seeded from the
// in-code catalog; custom rows are created through the API with an external
// digest-pinned image.
type SandboxProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Image is the digest-pinned reference (repo@sha256:…). Empty means the
	// profile is not built yet — run_command under it fails with an explicit
	// error instead of silently substituting another image (§4.3).
	Image string `json:"image,omitempty"`
	// Base names the known build recipe the profile composes on ("base",
	// "node", "python", "browser-e2e"); Packages are the extra OS packages
	// layered on top. A recipe-based custom profile has Image empty until its
	// build pins the digest; an external-image profile carries Image and no
	// Base. Builtins own their catalog recipes and never set Base.
	Base       string   `json:"base,omitempty"`
	Packages   []string `json:"packages,omitempty"`
	Toolchains []string `json:"toolchains,omitempty"`
	// DockerfileOverride is the operator recipe (D1): a full Dockerfile the
	// operator authors instead of base+packages. Exactly one recipe source is
	// allowed — base[+packages] XOR this field XOR Image (D2.1); builtin rows
	// never carry it (D2.4) and the model-driven builder has no such field
	// (D2.2). kernel/sandbox.OverrideDefinition is the build-side authority;
	// ValidateSandboxProfile mirrors its checks for early save-time errors.
	DockerfileOverride string `json:"dockerfile_override,omitempty"`
	// Resource overrides; empty = the runner's installation defaults.
	Memory      string `json:"memory,omitempty"`
	CPUs        string `json:"cpus,omitempty"`
	ScratchSize string `json:"scratch_size,omitempty"`
	TmpSize     string `json:"tmp_size,omitempty"`
	// CacheEnv entries are KEY=VALUE pairs pointing toolchain caches at /cache
	// (the persistent per-project cache mount). Values outside /cache are
	// rejected at save time (§12).
	CacheEnv []string `json:"cache_env,omitempty"`
	// DockerSocket mounts the host daemon socket into sandbox containers of
	// this profile (Docker-out-of-Docker, §5.3). The socket is host-root-
	// equivalent, so this is an explicit operator decision, custom profiles
	// only, and it never travels through the model-driven profile builder.
	DockerSocket bool `json:"docker_socket,omitempty"`
	// Enabled is the uniform on/off switch (the trigger pattern): a disabled
	// profile cannot be resolved for new runs; agents bound to it fail loudly
	// at start instead of silently landing in another image.
	Enabled   bool `json:"enabled"`
	Builtin   bool `json:"builtin"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ErrProfileBuiltin reports an attempt to delete or rewrite a builtin profile.
var ErrProfileBuiltin = errors.New("sandbox profile is builtin")

// ErrProfileInUse reports a delete blocked by agents still assigned to the
// profile: removing it would strand their runs on a missing image.
var ErrProfileInUse = errors.New("sandbox profile is assigned to agents")

// ErrProfileUnprocessable reports a recipe-source violation of the operator
// override rules (D2.1/D2.4): the API maps it to 422 with the explicit
// reason instead of silently dropping or substituting a source.
var ErrProfileUnprocessable = errors.New("sandbox profile recipe source is inconsistent")

// sandboxProfileIDPattern is the profile id shape; kernel/sandbox keeps a
// mirror (it travels into build tags).
var sandboxProfileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// ValidSandboxBases is the closed set of base recipes a custom profile may
// compose on: the three builtin profiles plus the embedded minimal alpine.
// kernel/sandbox.BaseDefinition is the build-side authority; a cmd-level test
// keeps the two catalogs from drifting apart.
var ValidSandboxBases = []string{"base", "node", "python", "browser-e2e"}

// BuiltinSandboxProfiles is the in-code catalog (§5). Images stay empty until
// an administrator builds them through POST …/{id}/build, which pins the
// digest into the row.
func BuiltinSandboxProfiles() []SandboxProfile {
	return []SandboxProfile{
		{
			ID:          "node",
			Name:        "Node 22",
			Description: "Node.js 22 + Python 3 с git, curl, jq, bash, ripgrep, unzip/tar/7z — фронтенд-сборки, npm-скрипты, JS-инструменты и офисный пакет скилов.",
			Toolchains:  []string{"node"},
			CacheEnv:    []string{"NPM_CONFIG_CACHE=/cache/npm"},
			Builtin:     true,
		},
		{
			ID:          "python",
			Name:        "Python 3.12",
			Description: "Python 3.12 с git, curl, jq, bash, ripgrep, unzip/tar/7z — скрипты, тесты, data-обработка и офисный пакет скилов.",
			Toolchains:  []string{"python"},
			CacheEnv:    []string{"PIP_CACHE_DIR=/cache/pip", "UV_CACHE_DIR=/cache/uv"},
			Builtin:     true,
		},
		{
			ID:          "browser-e2e",
			Name:        "Browser E2E (Playwright)",
			Description: "Node 22 + Playwright с предустановленным Chromium — браузерные E2E-тесты и скриншоты.",
			Toolchains:  []string{"node", "browser"},
			// Chromium is memory-hungry: the browser profile raises the
			// container budget instead of every agent paying for it.
			Memory:      "2g",
			ScratchSize: "1g",
			TmpSize:     "512m",
			CacheEnv:    []string{"NPM_CONFIG_CACHE=/cache/npm", "PLAYWRIGHT_BROWSERS_PATH=/cache/ms-playwright"},
			Builtin:     true,
		},
	}
}

// CacheSubdirs extracts the /cache subdirectories referenced by cache env
// entries so the runner can pre-create them: "NPM_CONFIG_CACHE=/cache/npm"
// yields "npm". Duplicates collapse; entries not under /cache are skipped
// (validation rejects them earlier, this is the defensive read side).
func CacheSubdirs(cacheEnv []string) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, entry := range cacheEnv {
		value := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			value = entry[idx+1:]
		}
		value = strings.TrimSuffix(strings.TrimPrefix(value, "/cache/"), "/")
		if value == "" || strings.Contains(value, "/") || seen[value] {
			continue
		}
		seen[value] = true
		dirs = append(dirs, value)
	}
	return dirs
}

// ValidateSandboxProfile checks a profile before save: id shape, required
// name, digest-pinned image, byte-size resource fields and KEY=VALUE cache
// entries pointing under /cache.
func ValidateSandboxProfile(p SandboxProfile) error {
	if !sandboxProfileIDPattern.MatchString(p.ID) {
		return fmt.Errorf("profile id must match %s", sandboxProfileIDPattern.String())
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("profile name is required")
	}
	if p.Image != "" && !IsDigestPinnedImage(p.Image) {
		return fmt.Errorf("image %q must be pinned by a sha256 digest (repo@sha256:…)", p.Image)
	}
	for _, size := range []struct{ field, value string }{
		{"memory", p.Memory}, {"cpus", p.CPUs}, {"scratch_size", p.ScratchSize}, {"tmp_size", p.TmpSize},
	} {
		if size.value != "" && !isValidResourceValue(size.field, size.value) {
			return fmt.Errorf("%s %q is not a valid resource value", size.field, size.value)
		}
	}
	for _, entry := range p.CacheEnv {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			return fmt.Errorf("cache_env entry %q must be KEY=VALUE", entry)
		}
		value := entry[idx+1:]
		if !strings.HasPrefix(value, "/cache/") || strings.HasSuffix(value, "/") {
			return fmt.Errorf("cache_env %q must point inside /cache/ (e.g. /cache/npm)", entry)
		}
	}
	// The operator override is one of three mutually exclusive recipe
	// sources (D2.1): base[+packages] XOR dockerfile_override XOR an
	// external image. Mixing sources is an explicit 422-class error — a
	// silent substitution would rebuild a different sandbox than the one
	// the operator sees.
	if p.DockerfileOverride != "" {
		if p.Base != "" || len(p.Packages) > 0 {
			return fmt.Errorf("%w: dockerfile_override excludes base and packages — clear them or drop the override", ErrProfileUnprocessable)
		}
		if p.Image != "" {
			return fmt.Errorf("%w: dockerfile_override excludes a direct image — the build pins the digest; clear image or drop the override", ErrProfileUnprocessable)
		}
		if p.Builtin {
			return fmt.Errorf("%w: builtin profiles never take a dockerfile override — the catalog ships with kernel updates", ErrProfileUnprocessable)
		}
		if err := ValidSandboxDockerfile(p.DockerfileOverride); err != nil {
			return fmt.Errorf("dockerfile_override: %w", err)
		}
	}
	// Recipe-based vs external-image custom profiles are mutually exclusive:
	// a recipe's image is produced by the build, never typed by hand.
	if p.Base != "" {
		if p.Image != "" {
			return fmt.Errorf("profile with a base recipe must not set an image directly; build it instead")
		}
		if p.Builtin {
			return fmt.Errorf("builtin profiles cannot use a base recipe")
		}
		var known bool
		for _, b := range ValidSandboxBases {
			if p.Base == b {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown sandbox base %q (known: %s)", p.Base, strings.Join(ValidSandboxBases, ", "))
		}
		for _, name := range p.Packages {
			if !ValidSandboxPackageName(name) {
				return fmt.Errorf("package name %q is not a valid apk/apt package", name)
			}
		}
	} else if len(p.Packages) > 0 {
		return fmt.Errorf("packages require a base recipe")
	}
	// The socket passthrough hands the host daemon to whatever runs in the
	// sandbox; it is a custom-profile capability the operator types in, never
	// a builtin default and never a model decision.
	if p.DockerSocket && p.Builtin {
		return fmt.Errorf("builtin profiles cannot mount the docker socket")
	}
	return nil
}

// validSandboxPackageName allows only characters that appear in apk/apt
// package names; kernel/sandbox's build-side validation is the stricter
// authority, this mirrors it so bad names fail at save time.
var validSandboxPackageName = regexp.MustCompile(`^[a-z0-9][a-z0-9.+_-]*$`)

// ValidSandboxPackageName reports whether name is a safe OS package name.
func ValidSandboxPackageName(name string) bool { return validSandboxPackageName.MatchString(name) }

// maxSandboxDockerfileBytes mirrors kernel/sandbox's override size budget.
const maxSandboxDockerfileBytes = 16 * 1024

// sandboxParserDirective mirrors kernel/sandbox's parserDirective: a
// # syntax= / # escape= comment (D7.1 forbidden in an operator recipe).
var sandboxParserDirective = regexp.MustCompile(`(?i)^#\s*(syntax|escape)\s*=`)

// sandboxDirectiveFormComment mirrors kernel/sandbox's directiveFormComment:
// any directive-shaped comment (# key=…) before the first instruction — the
// parser treats that shape as a directive generically (F-01), so the mirror
// blocks it too.
var sandboxDirectiveFormComment = regexp.MustCompile(`(?i)^#\s*([a-z][a-z0-9]*)\s*=`)

// sandboxNormalizeDockerfileText mirrors kernel/sandbox's
// normalizeDockerfileText: strip the UTF-8 BOM and fold \r\n to \n the way
// the docker parser does before interpreting the file (F-01).
func sandboxNormalizeDockerfileText(text string) string {
	return strings.ReplaceAll(strings.TrimPrefix(text, "\xef\xbb\xbf"), "\r\n", "\n")
}

// sandboxDockerfileInstruction mirrors kernel/sandbox's dockerfileInstruction:
// one logical line joined the way the docker parser joins continuations
// (byte concatenation, no separator — F-02), with the physical line of each
// segment for line-numbered errors.
type sandboxDockerfileInstruction struct {
	text         string
	segmentLines []sandboxSegmentStart
}

type sandboxSegmentStart struct {
	offset int
	line   int
}

func (u sandboxDockerfileInstruction) line() int {
	return u.segmentLines[0].line
}

func (u sandboxDockerfileInstruction) lineAt(offset int) int {
	line := u.segmentLines[0].line
	for _, seg := range u.segmentLines {
		if seg.offset > offset {
			break
		}
		line = seg.line
	}
	return line
}

// sandboxDockerfileComment mirrors kernel/sandbox's dockerfileComment.
type sandboxDockerfileComment struct {
	line        int
	text        string
	beforeFirst bool
}

// sandboxParseDockerfileInstructions mirrors kernel/sandbox's
// parseDockerfileInstructions.
func sandboxParseDockerfileInstructions(text string) ([]sandboxDockerfileInstruction, []sandboxDockerfileComment) {
	var (
		instructions []sandboxDockerfileInstruction
		comments     []sandboxDockerfileComment
		current      sandboxDockerfileInstruction
		open         bool // an instruction is in continuation
	)
	for idx, raw := range strings.Split(text, "\n") {
		physLine := idx + 1
		line := strings.TrimRight(raw, "\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if !open {
				comments = append(comments, sandboxDockerfileComment{line: physLine, text: trimmed, beforeFirst: len(instructions) == 0})
			}
			continue
		}
		if !open {
			line = strings.TrimLeftFunc(line, unicode.IsSpace)
			current = sandboxDockerfileInstruction{}
		}
		content, continued := sandboxTrimDockerfileContinuation(line)
		current.segmentLines = append(current.segmentLines, sandboxSegmentStart{offset: len(current.text), line: physLine})
		current.text += content
		if !continued {
			instructions = append(instructions, current)
			open = false
			continue
		}
		open = true
	}
	if open {
		instructions = append(instructions, current)
	}
	return instructions, comments
}

// sandboxTrimDockerfileContinuation mirrors kernel/sandbox's
// trimDockerfileContinuation (docker's trimContinuationCharacter).
func sandboxTrimDockerfileContinuation(line string) (string, bool) {
	end := len(line)
	for end > 0 && (line[end-1] == ' ' || line[end-1] == '\t') {
		end--
	}
	if end == 0 || line[end-1] != '\\' {
		return line, false
	}
	if end >= 2 && line[end-2] == '\\' {
		return line, false // an escaped backslash is literal
	}
	return line[:end-1], true
}

// ValidSandboxDockerfile mirrors kernel/sandbox.ValidateDockerfileOverride —
// the same D3/D7 rules at save time so a bad operator recipe fails at the API
// with a line-numbered error instead of minutes later at the docker daemon:
// non-empty, ≤16 KiB, UTF-8 without NUL, at least one FROM (multi-stage
// allowed), no ADD / COPY without --from / ONBUILD (the build has no
// context), no parser directives — BOM/CRLF-proof, any # key= before the
// first instruction (F-01) — and no RUN --network=host / --security=
// insecure|privileged / --mount except type=cache, scanned on
// continuation-joined text so a token cut by a line continuation is whole
// again (F-02…F-04). A cmd-level test keeps the two validators from
// drifting apart.
func ValidSandboxDockerfile(text string) error {
	text = sandboxNormalizeDockerfileText(text)
	if strings.TrimSpace(text) == "" {
		return errors.New("dockerfile override is required (the operator recipe is empty)")
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("строка %d: dockerfile override must be valid UTF-8", 1+strings.Count(text[:firstInvalidUTF8(text)], "\n"))
	}
	if idx := strings.IndexByte(text, 0); idx >= 0 {
		return fmt.Errorf("строка %d: недопустимый символ NUL в тексте dockerfile", 1+strings.Count(text[:idx], "\n"))
	}
	if len(text) > maxSandboxDockerfileBytes {
		return fmt.Errorf("строка %d: dockerfile override слишком большой: %d байт > %d (16 KiB)", sandboxSizeViolationLine(text), len(text), maxSandboxDockerfileBytes)
	}
	instructions, comments := sandboxParseDockerfileInstructions(text)
	var (
		fromSeen   bool
		violations []string
	)
	for _, comment := range comments {
		if match := sandboxParserDirective.FindStringSubmatch(comment.text); match != nil {
			violations = append(violations, sandboxDirectiveViolation(comment.line, strings.ToLower(match[1])))
			continue
		}
		if !comment.beforeFirst {
			continue
		}
		if match := sandboxDirectiveFormComment.FindStringSubmatch(comment.text); match != nil {
			violations = append(violations, sandboxDirectiveViolation(comment.line, strings.ToLower(match[1])))
		}
	}
	for _, instr := range instructions {
		fields := strings.Fields(instr.text)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			fromSeen = true
		case "ADD":
			violations = append(violations, fmt.Sprintf("строка %d: ADD запрещён — сборка идёт без контекста (только Dockerfile через stdin), скачивание и распаковка архивов в образ недоступны; используйте RUN", instr.line()))
		case "COPY":
			if !strings.Contains(strings.ToLower(instr.text), "--from=") {
				violations = append(violations, fmt.Sprintf("строка %d: COPY без --from не работает — build context отсутствует, копировать можно только из другого этапа сборки (COPY --from=<stage>)", instr.line()))
			}
		case "ONBUILD":
			violations = append(violations, fmt.Sprintf("строка %d: ONBUILD запрещён — триггерные инструкции отложат неизвестное действие до сборки образа-потребителя", instr.line()))
		case "RUN":
			violations = append(violations, sandboxRunFlagViolations(instr)...)
		}
	}
	if !fromSeen {
		violations = append(violations, "ни одной инструкции FROM во всём файле: образ должен начинаться с базового (FROM …), включая multi-stage сборки")
	}
	if len(violations) > 0 {
		return errors.New(strings.Join(violations, "; "))
	}
	return nil
}

// sandboxDockerfileToken mirrors kernel/sandbox's dockerfileToken.
type sandboxDockerfileToken struct {
	text   string
	offset int
}

// sandboxInstructionTokens mirrors kernel/sandbox's instructionTokens.
func sandboxInstructionTokens(instr sandboxDockerfileInstruction) []sandboxDockerfileToken {
	var tokens []sandboxDockerfileToken
	for offset := 0; offset < len(instr.text); {
		r, size := utf8.DecodeRuneInString(instr.text[offset:])
		if unicode.IsSpace(r) {
			offset += size
			continue
		}
		start := offset
		for offset < len(instr.text) {
			r, size = utf8.DecodeRuneInString(instr.text[offset:])
			if unicode.IsSpace(r) {
				break
			}
			offset += size
		}
		tokens = append(tokens, sandboxDockerfileToken{text: instr.text[start:offset], offset: start})
	}
	return tokens
}

// sandboxRunFlagViolations mirrors kernel/sandbox's runFlagViolations: the
// RUN leading flag section scanned against the D7 deny-list.
func sandboxRunFlagViolations(instr sandboxDockerfileInstruction) []string {
	var out []string
	tokens := sandboxInstructionTokens(instr)
	if len(tokens) == 0 {
		return nil
	}
	// tokens[0] is the RUN keyword itself — the flag section starts right
	// after it; without skipping it the first HasPrefix check would break
	// before any flag is ever seen (mirror of kernel runFlagViolations).
	for _, token := range tokens[1:] {
		if !strings.HasPrefix(token.text, "--") {
			break
		}
		if violation := sandboxRunFlagViolation(token.text, instr.lineAt(token.offset)); violation != "" {
			out = append(out, violation)
		}
	}
	return out
}

// sandboxRunFlagViolation mirrors kernel/sandbox's runFlagViolation: the
// D7.2 deny-list for RUN flags (F-02…F-04a).
func sandboxRunFlagViolation(token string, line int) string {
	name, value, hasValue := token, "", false
	if eq := strings.IndexByte(token, '='); eq >= 0 {
		name, value, hasValue = token[:eq], strings.Trim(token[eq+1:], `"'`), true
	}
	switch strings.ToLower(name) {
	case "--network":
		if hasValue && strings.EqualFold(value, "host") {
			return sandboxRunHostNetworkViolation(line)
		}
	case "--security":
		if hasValue && (strings.EqualFold(value, "insecure") || strings.EqualFold(value, "privileged")) {
			return fmt.Sprintf("строка %d: RUN --security=%s запрещён — шаг сборки выполняется без seccomp/AppArmor-изоляции демона; уберите флаг", line, strings.ToLower(value))
		}
	case "--mount":
		// A bare --mount (no '=') and an empty value both arrive as "" and
		// are rejected by the same strict parse with the same message.
		if ok, reason := sandboxIsCacheMount(value); !ok {
			return fmt.Sprintf("строка %d: RUN --mount запрещён — %s; допустим только один type=cache без дублей ключей; уберите флаг либо оставьте type=cache", line, reason)
		}
	}
	return ""
}

// sandboxCacheMountIDPattern mirrors kernel/sandbox's cacheMountIDPattern:
// 1..64 chars of letters, digits, dot, dash and underscore — a path shape
// never matches, so an id cannot steer the mount out of its cache directory
// (F-04b read as a path vector; the shared well-formed id is the audit's
// accepted residue of F-04b).
var sandboxCacheMountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// sandboxIsCacheMount mirrors kernel/sandbox's isCacheMount: the strict
// F-04a parse of a RUN --mount option set. Buildkit resolves duplicated
// keys by overwrite (duplicate-key semantics), so "any type=cache" would
// pass type=cache,type=bind; the mirror therefore requires exactly one
// cache type spelled once — no repeated key, only cache-mount allowlisted
// keys (type,target,id,sharing,mode,uid,gid), type=cache after unquoting
// the option value and folding case, sharing ∈ {shared,private,locked},
// id ∈ [A-Za-z0-9_.-]{1,64} and digits-only mode/uid/gid. An empty value —
// the bare --mount flag included — is rejected: an empty option set is not
// a cache mount.
func sandboxIsCacheMount(value string) (bool, string) {
	if strings.TrimSpace(value) == "" {
		return false, "значение флага отсутствует или пустое"
	}
	seen := make(map[string]bool)
	typeSeen := false
	for _, option := range strings.Split(value, ",") {
		opt := strings.TrimSpace(option)
		if opt == "" {
			return false, "пустая опция в списке (двойная запятая)"
		}
		eq := strings.IndexByte(opt, '=')
		if eq < 0 {
			return false, fmt.Sprintf("опция %q без = (нужен key=value)", opt)
		}
		key := strings.ToLower(strings.TrimSpace(opt[:eq]))
		val := strings.Trim(strings.TrimSpace(opt[eq+1:]), `"'`)
		if seen[key] {
			return false, fmt.Sprintf("дубль ключа опции %q", key)
		}
		seen[key] = true
		switch key {
		case "type":
			typeSeen = true
			if !strings.EqualFold(val, "cache") {
				return false, fmt.Sprintf("тип %q вместо cache", val)
			}
		case "target":
			if val == "" {
				return false, "пустое значение target"
			}
		case "id":
			if !sandboxCacheMountIDPattern.MatchString(val) {
				return false, fmt.Sprintf("id %q вне [A-Za-z0-9_.-]{1,64}", val)
			}
		case "sharing":
			if val != "shared" && val != "private" && val != "locked" {
				return false, fmt.Sprintf("sharing %q — допустимы shared, private, locked", val)
			}
		case "mode", "uid", "gid":
			if val == "" || strings.Trim(val, "0123456789") != "" {
				return false, fmt.Sprintf("%s %q должно быть числом", key, val)
			}
		default:
			return false, fmt.Sprintf("ключ %q не входит в список разрешённых (type,target,id,sharing,mode,uid,gid)", key)
		}
	}
	if !typeSeen {
		return false, "нет ключа type"
	}
	return true, ""
}

// sandboxDirectiveViolation mirrors kernel/sandbox's directiveViolation.
func sandboxDirectiveViolation(line int, name string) string {
	return fmt.Sprintf("строка %d: parser-директива #%s= запрещена — она меняет разбор/фронтенд сборки, а смена frontend-образа влечёт его pull по сети на хост", line, name)
}

// sandboxRunHostNetworkViolation mirrors kernel/sandbox's runHostNetworkViolation.
func sandboxRunHostNetworkViolation(line int) string {
	return fmt.Sprintf("строка %d: RUN --network=host запрещён — команда сборки выполняется в network-неймспейсе хоста; уберите флаг", line)
}

// firstInvalidUTF8 mirrors kernel/sandbox's helper: byte offset of the first
// malformed rune, for line-numbered errors.
func firstInvalidUTF8(text string) int {
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return len(text)
}

// sandboxSizeViolationLine mirrors kernel/sandbox's helper: the 1-based line
// on which the text crosses the size budget.
func sandboxSizeViolationLine(text string) int {
	total := 0
	for i, line := range strings.SplitAfter(text, "\n") {
		total += len(line)
		if total > maxSandboxDockerfileBytes {
			return i + 1
		}
	}
	return 1 + strings.Count(text, "\n")
}

// IsDigestPinnedImage reports whether ref is repo@sha256:<64 hex>. It mirrors
// kernel/sandbox's validateImage so both sides reject mutable tags without
// importing each other.
func IsDigestPinnedImage(ref string) bool {
	marker := strings.LastIndex(ref, "@sha256:")
	if marker <= 0 || len(ref)-marker-len("@sha256:") != 64 {
		return false
	}
	for _, c := range ref[marker+len("@sha256:"):] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// isValidResourceValue accepts cpus as a decimal fraction and sizes as bytes
// or a k/m/g unit — the shapes docker accepts for --cpus/--memory/tmpfs size.
func isValidResourceValue(field, value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if field == "cpus" {
		return regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`).MatchString(trimmed)
	}
	digits := strings.TrimRight(trimmed, "bBkKmMgG")
	return digits != "" && strings.Trim(digits, "0123456789") == ""
}

const sandboxProfileColumns = `id, name, description, image, base, packages, toolchains, memory, cpus, scratch_size, tmp_size, cache_env, docker_socket, dockerfile_override, enabled, builtin, created_at, updated_at`

func scanSandboxProfile(row interface{ Scan(dest ...any) error }) (SandboxProfile, error) {
	var p SandboxProfile
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Image, &p.Base, &p.Packages, &p.Toolchains,
		&p.Memory, &p.CPUs, &p.ScratchSize, &p.TmpSize, &p.CacheEnv, &p.DockerSocket, &p.DockerfileOverride, &p.Enabled, &p.Builtin, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, err
	}
	return p, nil
}

// SeedSandboxProfiles inserts the builtin catalog rows that are missing.
// ON CONFLICT DO NOTHING keeps an admin-built image and any local tweaks —
// seeding never overwrites.
// SetSandboxProfileEnabled flips the uniform on/off switch: a disabled
// profile cannot be resolved for new runs. The profile builders and saves
// never carry the flag, so rebuilding an image cannot disable it.
func (s *Store) SetSandboxProfileEnabled(ctx context.Context, id string, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_sandbox_profile SET enabled=$2, updated_at=now() WHERE id=$1`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SeedSandboxProfiles(ctx context.Context) error {
	for _, p := range BuiltinSandboxProfiles() {
		now := time.Now().UTC()
		_, err := s.pool.Exec(ctx,
			`INSERT INTO workspace_sandbox_profile (id, name, description, image, base, packages, toolchains, memory, cpus, scratch_size, tmp_size, cache_env, dockerfile_override, builtin, created_at, updated_at)
			 VALUES ($1,$2,$3,'','','{}',$4,$5,$6,$7,$8,$9,'',TRUE,$10,$10)
			 ON CONFLICT (id) DO NOTHING`,
			p.ID, p.Name, p.Description, p.Toolchains, p.Memory, p.CPUs, p.ScratchSize, p.TmpSize, p.CacheEnv, now)
		if err != nil {
			return fmt.Errorf("seed sandbox profile %s: %w", p.ID, err)
		}
	}
	return nil
}

func (s *Store) ListSandboxProfiles(ctx context.Context) ([]SandboxProfile, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+sandboxProfileColumns+` FROM workspace_sandbox_profile ORDER BY builtin DESC, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SandboxProfile
	for rows.Next() {
		p, err := scanSandboxProfile(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) GetSandboxProfile(ctx context.Context, id string) (SandboxProfile, error) {
	return scanSandboxProfile(s.pool.QueryRow(ctx,
		`SELECT `+sandboxProfileColumns+` FROM workspace_sandbox_profile WHERE id = $1`, id))
}

// CreateSandboxProfile stores a custom profile. Builtin ids are reserved by
// the catalog, so a custom row colliding with one is rejected up front.
func (s *Store) CreateSandboxProfile(ctx context.Context, p *SandboxProfile) error {
	for _, builtin := range BuiltinSandboxProfiles() {
		if p.ID == builtin.ID {
			return fmt.Errorf("%w: %s", ErrProfileBuiltin, p.ID)
		}
	}
	if err := ValidateSandboxProfile(*p); err != nil {
		return err
	}
	p.Builtin = false
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_sandbox_profile (id, name, description, image, base, packages, toolchains, memory, cpus, scratch_size, tmp_size, cache_env, docker_socket, dockerfile_override, builtin, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,FALSE,$15,$15)`,
		p.ID, p.Name, p.Description, p.Image, p.Base, p.Packages, p.Toolchains, p.Memory, p.CPUs, p.ScratchSize, p.TmpSize, p.CacheEnv, p.DockerSocket, p.DockerfileOverride, now)
	return err
}

// UpdateSandboxProfile rewrites a custom profile. A builtin row accepts only
// resource overrides (memory, cpus, scratch/tmp sizes) and cache env: the
// catalog identity (name, description, toolchains) travels with kernel
// updates and the image is pinned by build, so a PUT never rewrites them.
func (s *Store) UpdateSandboxProfile(ctx context.Context, p SandboxProfile) error {
	existing, err := s.GetSandboxProfile(ctx, p.ID)
	if err != nil {
		return err
	}
	p = prepareSandboxProfileUpdate(existing, p)
	if err := ValidateSandboxProfile(p); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_sandbox_profile
		 SET name=$2, description=$3, image=$4, base=$5, packages=$6, toolchains=$7, memory=$8, cpus=$9, scratch_size=$10, tmp_size=$11, cache_env=$12, docker_socket=$13, dockerfile_override=$14, updated_at=$15
		 WHERE id=$1`,
		p.ID, p.Name, p.Description, p.Image, p.Base, p.Packages, p.Toolchains, p.Memory, p.CPUs, p.ScratchSize, p.TmpSize, p.CacheEnv, p.DockerSocket, p.DockerfileOverride, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// prepareSandboxProfileUpdate folds an incoming PUT onto the stored row.
// A builtin keeps its catalog identity and image; its recipe fields are not
// touched — an override attempt stays on the incoming row so validation
// rejects it explicitly (a silent drop would be a substitution, §4.3).
// Editing a recipe invalidates the pinned image: the digest on the row would
// silently not match what the recipe now produces — the same holds for the
// operator override text (D2.3).
func prepareSandboxProfileUpdate(existing, incoming SandboxProfile) SandboxProfile {
	if existing.Builtin {
		incoming.Name = existing.Name
		incoming.Description = existing.Description
		incoming.Toolchains = existing.Toolchains
		incoming.Image = existing.Image
		incoming.Base = ""
		incoming.Packages = nil
		incoming.DockerSocket = existing.DockerSocket
		incoming.Builtin = true
		return incoming
	}
	incoming.Builtin = false
	if existing.Base != incoming.Base ||
		!equalStrings(existing.Packages, incoming.Packages) ||
		existing.DockerfileOverride != incoming.DockerfileOverride {
		incoming.Image = ""
	}
	return incoming
}

// SetSandboxProfileImage pins a freshly built digest into a builtin profile
// and refreshes the catalog metadata (name, description, toolchains) from the
// in-code catalog. Locally tuned resource overrides and cache env survive the
// rebuild: they are editable through the API and belong to the deployment.
func (s *Store) SetSandboxProfileImage(ctx context.Context, id, image string) error {
	var builtin SandboxProfile
	found := false
	for _, p := range BuiltinSandboxProfiles() {
		if p.ID == id {
			builtin = p
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s is not a builtin profile", ErrProfileBuiltin, id)
	}
	if !IsDigestPinnedImage(image) {
		return fmt.Errorf("image %q must be pinned by a sha256 digest", image)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_sandbox_profile
		 SET image=$2, name=$3, description=$4, toolchains=$5, updated_at=now()
		 WHERE id=$1 AND builtin = TRUE`,
		id, image, builtin.Name, builtin.Description, builtin.Toolchains)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetSandboxProfile(ctx, id); err != nil {
			return err
		}
		return errors.New("sandbox profile row did not update")
	}
	return nil
}

// SetExternalProfileImage pins a pulled external image into a custom
// profile without a base recipe (docs/sandbox-profiles.md §5.1 phase 2):
// docker pull resolved the reference to a local digest, the row carries it
// like a hand-typed one — but audited and journaled.
func (s *Store) SetExternalProfileImage(ctx context.Context, id, image string) error {
	existing, err := s.GetSandboxProfile(ctx, id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return fmt.Errorf("%w: %s is a builtin profile", ErrProfileBuiltin, id)
	}
	if existing.Base != "" {
		return fmt.Errorf("profile %s composes on a base recipe; build it instead of pulling", id)
	}
	if existing.DockerfileOverride != "" {
		return fmt.Errorf("profile %s carries an operator dockerfile override; build it instead of pulling", id)
	}
	if !IsDigestPinnedImage(image) {
		return fmt.Errorf("image %q must be pinned by a sha256 digest", image)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_sandbox_profile SET image=$2, updated_at=now() WHERE id=$1 AND builtin = FALSE`,
		id, image)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCustomProfileImage pins a freshly built digest into a recipe-based
// custom profile — composed from a base or built from the operator's
// dockerfile override. Unlike the builtin variant it touches no metadata —
// a custom row owns its name/description/toolchains.
func (s *Store) SetCustomProfileImage(ctx context.Context, id, image string) error {
	existing, err := s.GetSandboxProfile(ctx, id)
	if err != nil {
		return err
	}
	if existing.Builtin {
		return fmt.Errorf("%w: %s is a builtin profile", ErrProfileBuiltin, id)
	}
	if existing.Base == "" && existing.DockerfileOverride == "" {
		return fmt.Errorf("profile %s has no build recipe; set its image reference directly", id)
	}
	if !IsDigestPinnedImage(image) {
		return fmt.Errorf("image %q must be pinned by a sha256 digest", image)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_sandbox_profile SET image=$2, updated_at=now() WHERE id=$1 AND builtin = FALSE`,
		id, image)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// equalStrings compares two string slices order-sensitively.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DeleteSandboxProfile removes a custom profile. Refusing when agents still
// reference it keeps assigned cards from pointing at a missing image.
func (s *Store) DeleteSandboxProfile(ctx context.Context, id string) error {
	profile, err := s.GetSandboxProfile(ctx, id)
	if err != nil {
		return err
	}
	if profile.Builtin {
		return fmt.Errorf("%w: %s", ErrProfileBuiltin, id)
	}
	agents, err := s.ListAllAgents(ctx)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.SandboxProfile == id {
			return fmt.Errorf("%w: %s is used by agent %s", ErrProfileInUse, id, a.ID)
		}
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_sandbox_profile WHERE id=$1 AND builtin = FALSE`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
