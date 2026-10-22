package sandbox

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Operator recipe override (docs/tasks/2026-10-21-sandbox-dockerfile-editing,
// D1/D3): a custom profile may carry a full Dockerfile authored by the
// operator instead of base+packages. The build has no context (stdin-only
// docker build -), so the text is validated here — the strict build-side
// authority; workspace mirrors these checks for early save-time errors and a
// cross-validator test keeps the two from drifting.

// maxOverrideBytes bounds the override text: 16 KiB is far beyond any sane
// toolchain recipe and keeps the row/payload small.
const maxOverrideBytes = 16 * 1024

// parserDirective matches a Dockerfile parser directive comment
// (# syntax=…, # escape=…). D7 forbids both in an operator recipe: switching
// the build frontend means a network pull of an arbitrary frontend image
// onto the host, and the escape directive silently changes how the whole
// file is parsed.
var parserDirective = regexp.MustCompile(`(?i)^#\s*(syntax|escape)\s*=`)

// directiveFormComment matches any directive-shaped comment (# key=…). The
// docker parser recognizes that shape generically before the first
// instruction, so blocking only syntax/escape would leave a different
// directive spelling (or a future directive) an unreviewed frontend switch
// (F-01). After the first instruction the same comment is ordinary text.
var directiveFormComment = regexp.MustCompile(`(?i)^#\s*([a-z][a-z0-9]*)\s*=`)

// normalizeDockerfileText applies the normalizations the docker parser
// applies before interpreting the file (F-01): the UTF-8 BOM is discarded
// and \r\n breaks become \n. Without this a BOM prefix hides the first
// line's directive from every check below — buildkit strips the BOM and
// still executes the directive — and a CRLF spelling has to be handled by
// every rule separately.
func normalizeDockerfileText(text string) string {
	return strings.ReplaceAll(strings.TrimPrefix(text, "\xef\xbb\xbf"), "\r\n", "\n")
}

// dockerfileInstruction is one logical line of the recipe: the byte-level
// concatenation of its physical segments exactly as the docker parser joins
// them — the trailing escape token (and trailing spaces after it) removed,
// segments glued without a separator, empty lines and comments inside a
// continuation skipped (F-02). segmentLines maps a text offset back to the
// physical line it came from, so violations keep naming real lines.
type dockerfileInstruction struct {
	text         string
	segmentLines []segmentStart
}

// segmentStart records that a text offset begins on a physical line.
type segmentStart struct {
	offset int
	line   int
}

// line is the physical line the instruction starts on — where a reader
// looks for the instruction and its first flags.
func (u dockerfileInstruction) line() int {
	return u.segmentLines[0].line
}

// lineAt returns the physical line the byte at offset belongs to, so a flag
// token cut by a continuation still names the line it sits on.
func (u dockerfileInstruction) lineAt(offset int) int {
	line := u.segmentLines[0].line
	for _, seg := range u.segmentLines {
		if seg.offset > offset {
			break
		}
		line = seg.line
	}
	return line
}

// dockerfileComment is a top-level comment line; beforeFirst records that no
// instruction had been seen yet, which is when the parser treats # key= as a
// directive.
type dockerfileComment struct {
	line        int
	text        string
	beforeFirst bool
}

// parseDockerfileInstructions splits the text into logical instructions the
// way moby/buildkit's parser does: a line whose last non-space character is
// an unescaped escape token continues onto the next line, comments and
// empty lines inside a continuation are dropped, and segments concatenate
// byte-for-byte with no separator inserted — `--network=ho\` + `st` is one
// token, while `--network=ho\` + `  st` keeps the whitespace and stays two.
// Top-level comments are collected separately with their physical lines.
func parseDockerfileInstructions(text string) ([]dockerfileInstruction, []dockerfileComment) {
	var (
		instructions []dockerfileInstruction
		comments     []dockerfileComment
		current      dockerfileInstruction
		open         bool // an instruction is in continuation
	)
	for idx, raw := range strings.Split(text, "\n") {
		physLine := idx + 1
		line := strings.TrimRight(raw, "\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// An empty line is skipped inside a continuation too — docker
			// warns but keeps joining.
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if !open {
				// A comment inside a continuation is dropped by the parser
				// and the instruction continues — only top-level comments
				// can be parser directives.
				comments = append(comments, dockerfileComment{line: physLine, text: trimmed, beforeFirst: len(instructions) == 0})
			}
			continue
		}
		if !open {
			// The first line of an instruction has its leading whitespace
			// trimmed; continuation lines keep theirs (docker parser).
			line = strings.TrimLeftFunc(line, unicode.IsSpace)
			current = dockerfileInstruction{}
		}
		content, continued := trimDockerfileContinuation(line)
		current.segmentLines = append(current.segmentLines, segmentStart{offset: len(current.text), line: physLine})
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

// trimDockerfileContinuation mirrors docker's trimContinuationCharacter: a
// line whose last non-space character is an unescaped escape token continues
// onto the next line — the token and the trailing spaces go, and nothing is
// inserted in their place. A doubled escape token at the end is a literal
// backslash, not a continuation.
func trimDockerfileContinuation(line string) (string, bool) {
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

// OverrideDefinition builds the profile definition for an operator-authored
// Dockerfile: the text travels verbatim into docker build stdin; the tag
// follows the custom-profile convention so rebuilds overwrite in place.
func OverrideDefinition(profileID, dockerfile string) (ProfileDefinition, error) {
	if !sandboxProfileIDPattern.MatchString(profileID) {
		return ProfileDefinition{}, fmt.Errorf("profile id %q does not match %s", profileID, sandboxProfileIDPattern.String())
	}
	if err := ValidateDockerfileOverride(dockerfile); err != nil {
		return ProfileDefinition{}, err
	}
	return ProfileDefinition{
		Profile:    profileID,
		Tag:        "temporality-sandbox-" + profileID + ":latest",
		Dockerfile: dockerfile,
	}, nil
}

// ValidateDockerfileOverride is the strict D3/D7 check on an operator
// Dockerfile: non-empty, size-bounded, UTF-8 without NUL, at least one FROM
// (multi-stage is a first-class scenario), and no instructions that cannot
// work without a build context (ADD, COPY without --from), that execute
// image code at build time (ONBUILD), or that escape the build sandbox
// (parser directives, RUN --network=host / --security=insecure|privileged /
// --mount except type=cache). The text is normalized the way the docker
// parser normalizes it (BOM stripped, CRLF folded, continuations joined by
// byte concatenation) before any rule runs, so no spelling hides a rule.
// Every violation carries its line number and reason.
func ValidateDockerfileOverride(text string) error {
	text = normalizeDockerfileText(text)
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("dockerfile override is required (the operator recipe is empty)")
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("строка %d: dockerfile override must be valid UTF-8", 1+strings.Count(text[:firstInvalidUTF8(text)], "\n"))
	}
	if idx := strings.IndexByte(text, 0); idx >= 0 {
		return fmt.Errorf("строка %d: недопустимый символ NUL в тексте dockerfile", 1+strings.Count(text[:idx], "\n"))
	}
	if len(text) > maxOverrideBytes {
		return fmt.Errorf("строка %d: dockerfile override слишком большой: %d байт > %d (16 KiB)", sizeViolationLine(text), len(text), maxOverrideBytes)
	}
	instructions, comments := parseDockerfileInstructions(text)
	var (
		fromSeen   bool
		violations []string
	)
	for _, comment := range comments {
		if match := parserDirective.FindStringSubmatch(comment.text); match != nil {
			violations = append(violations, directiveViolation(comment.line, strings.ToLower(match[1])))
			continue
		}
		if !comment.beforeFirst {
			continue
		}
		if match := directiveFormComment.FindStringSubmatch(comment.text); match != nil {
			violations = append(violations, directiveViolation(comment.line, strings.ToLower(match[1])))
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
			if !containsFromFlag(instr.text) {
				violations = append(violations, fmt.Sprintf("строка %d: COPY без --from не работает — build context отсутствует, копировать можно только из другого этапа сборки (COPY --from=<stage>)", instr.line()))
			}
		case "ONBUILD":
			violations = append(violations, fmt.Sprintf("строка %d: ONBUILD запрещён — триггерные инструкции отложат неизвестное действие до сборки образа-потребителя", instr.line()))
		case "RUN":
			violations = append(violations, runFlagViolations(instr)...)
		}
	}
	if !fromSeen {
		violations = append(violations, "ни одной инструкции FROM во всём файле: образ должен начинаться с базового (FROM …), включая multi-stage сборки")
	}
	if len(violations) > 0 {
		return fmt.Errorf("%s", strings.Join(violations, "; "))
	}
	return nil
}

// dockerfileToken is one whitespace-delimited token of the joined
// instruction text with its byte offset.
type dockerfileToken struct {
	text   string
	offset int
}

// instructionTokens splits the joined instruction text on whitespace — the
// tokenization buildkit's flag parser uses — keeping byte offsets so each
// flag names the physical line it came from.
func instructionTokens(instr dockerfileInstruction) []dockerfileToken {
	var tokens []dockerfileToken
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
		tokens = append(tokens, dockerfileToken{text: instr.text[start:offset], offset: start})
	}
	return tokens
}

// runFlagViolations scans the leading flag section of a RUN instruction —
// tokens up to the first non-flag — against the D7 deny-list. The section is
// scanned on the continuation-joined text (F-02), so a token cut by a line
// continuation is whole again before the check. Only the flag section counts:
// `RUN echo --network=host` is command content, not a flag.
func runFlagViolations(instr dockerfileInstruction) []string {
	var out []string
	tokens := instructionTokens(instr)
	if len(tokens) == 0 {
		return nil
	}
	// tokens[0] is the instruction keyword (RUN) itself — the flag section
	// starts right after it; without skipping it the first HasPrefix check
	// would break before any flag is ever seen.
	for _, token := range tokens[1:] {
		if !strings.HasPrefix(token.text, "--") {
			break
		}
		if violation := runFlagViolation(token.text, instr.lineAt(token.offset)); violation != "" {
			out = append(out, violation)
		}
	}
	return out
}

// runFlagViolation rejects the D7-denied RUN flags: --network=host runs the
// build command in the host's network namespace; --security=insecure and
// --security=privileged drop the daemon's seccomp/AppArmor confinement for
// the build step; any --mount except one strictly parsed type=cache option
// set either pulls sources without a build context or hands secrets/ssh
// agents to the build — only a cache mount stays inside the recipe's trust
// class (D7.2, F-02…F-04a). Buildkit parses RUN flags literally (no shell
// quoting), so the value comparison strips quotes and folds case on purpose —
// stricter than the daemon, never laxer.
func runFlagViolation(token string, line int) string {
	name, value, hasValue := token, "", false
	if eq := strings.IndexByte(token, '='); eq >= 0 {
		name, value, hasValue = token[:eq], strings.Trim(token[eq+1:], `"'`), true
	}
	switch strings.ToLower(name) {
	case "--network":
		if hasValue && strings.EqualFold(value, "host") {
			return runHostNetworkViolation(line)
		}
	case "--security":
		if hasValue && (strings.EqualFold(value, "insecure") || strings.EqualFold(value, "privileged")) {
			return fmt.Sprintf("строка %d: RUN --security=%s запрещён — шаг сборки выполняется без seccomp/AppArmor-изоляции демона; уберите флаг", line, strings.ToLower(value))
		}
	case "--mount":
		// A bare --mount (no '=') and an empty value both arrive as "" and
		// are rejected by the same strict parse with the same message.
		if ok, reason := isCacheMount(value); !ok {
			return fmt.Sprintf("строка %d: RUN --mount запрещён — %s; допустим только один type=cache без дублей ключей; уберите флаг либо оставьте type=cache", line, reason)
		}
	}
	return ""
}

// cacheMountIDPattern bounds a cache mount id: 1..64 chars of letters,
// digits, dot, dash and underscore. A path shape never matches, so an id
// cannot steer the mount out of its cache directory (F-04b read as a path
// vector). Note that any well-formed id stays shared between builds that
// spell it — cross-profile cache sharing is the accepted-with-justification
// residue of F-04b, not something this check can close.
var cacheMountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// isCacheMount strictly parses the option set of a RUN --mount flag (F-04a).
// Buildkit resolves the comma-separated key=value options with duplicate-key
// semantics — a repeated key overwrites the earlier one — so a scan for
// "any type=cache" would pass `type=cache,type=bind` whose effective type is
// bind. The accepted set is therefore exactly one cache type spelled once:
// no key may repeat, every key must belong to the cache-mount allowlist
// (type,target,id,sharing,mode,uid,gid), type must be cache after unquoting
// the option value and folding case, sharing must be shared/private/locked,
// id must match [A-Za-z0-9_.-]{1,64} and mode/uid/gid must be digits. It
// reports whether the value is that one cache mount and, when not, the
// reason for the line-numbered rejection. An empty value — the bare
// --mount flag included — is rejected here: an empty option set is not a
// cache mount.
func isCacheMount(value string) (bool, string) {
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
			if !cacheMountIDPattern.MatchString(val) {
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

// containsFromFlag reports whether a COPY line selects a source stage. The
// flag may sit anywhere before the source path; the JSON form cannot carry
// flags at all, so a quoted array without --from is rejected like a plain one.
func containsFromFlag(line string) bool {
	return strings.Contains(strings.ToLower(line), "--from=")
}

// directiveViolation renders the D7.1 rejection of a parser directive with
// its line number. The same shape covers every # key= spelling before the
// first instruction: the parser treats that comment shape as a directive
// generically, so the ban does too.
func directiveViolation(line int, name string) string {
	return fmt.Sprintf("строка %d: parser-директива #%s= запрещена — она меняет разбор/фронтенд сборки, а смена frontend-образа влечёт его pull по сети на хост", line, name)
}

// runHostNetworkViolation renders the D7.2 rejection of RUN … --network=host
// with its line number.
func runHostNetworkViolation(line int) string {
	return fmt.Sprintf("строка %d: RUN --network=host запрещён — команда сборки выполняется в network-неймспейсе хоста; уберите флаг", line)
}

// firstInvalidUTF8 returns the byte offset of the first malformed rune.
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

// sizeViolationLine returns the 1-based line on which the text crosses the
// size budget, so an oversized override points at a line like every other
// violation.
func sizeViolationLine(text string) int {
	total := 0
	for i, line := range strings.SplitAfter(text, "\n") {
		total += len(line)
		if total > maxOverrideBytes {
			return i + 1
		}
	}
	return 1 + strings.Count(text, "\n")
}
