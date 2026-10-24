package agent

// Behavior guard (docs/agent-behavior-monitoring.md, stages 1–2): a
// deterministic, LLM-free scanner over tool arguments, file-tool payloads and
// command output. It never blocks a call — it flags, and the supervisor queue
// decides. Precision beats recall here: every pattern below must be a
// near-certain signal, because a bored human disables the whole guard.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// behaviorHit is one scanner finding. Detail is human-readable and bounded;
// never includes the secret value itself.
type behaviorHit struct {
	Kind    string `json:"kind"`   // secret_leak | canary | danger_content | suspicious_arg | test_edit
	Detail  string `json:"detail"` // bounded human context (no secret values)
	Canary  bool   `json:"canary,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

const (
	behaviorKindSecretLeak    = "secret_leak"
	behaviorKindCanary        = "canary"
	behaviorKindDangerContent = "danger_content"
	behaviorKindSuspiciousArg = "suspicious_arg"
	behaviorKindTestEdit      = "test_edit"

	behaviorScanCap = 32 << 10 // per-field scan cap — payloads beyond this are truncated by callers anyway
)

// behaviorSecret is one secret the run has been granted, with the encodings a
// leak could travel in. Values are compared verbatim; short values (<8 chars)
// are skipped — they cannot be told apart from ordinary words.
type behaviorSecret struct {
	name      string
	canary    bool
	encodings []string // exact, base64, hex, urlencoded — all lowercase
}

func newBehaviorSecret(name, value string, canary bool) behaviorSecret {
	value = strings.TrimSpace(value)
	if len(value) < 8 {
		// Short values cannot be told apart from ordinary words — matching
		// them would be pure noise. Canary semantics come from the flag, not
		// from content matching, so short canaries are simply not scannable.
		return behaviorSecret{name: name, canary: canary, encodings: nil}
	}
	enc := []string{strings.ToLower(value)}
	enc = append(enc,
		strings.ToLower(base64.StdEncoding.EncodeToString([]byte(value))),
		strings.ToLower(hex.EncodeToString([]byte(value))),
		strings.ToLower(url.QueryEscape(value)),
	)
	return behaviorSecret{name: name, canary: canary, encodings: enc}
}

// scanSecretValues looks for granted-secret values (verbatim or trivially
// encoded) inside an untrusted text: command arguments, tool output or a file
// payload. A hit is near-certain: the values are unique random strings that
// cannot appear by accident, and the sandbox delivers secrets as env vars —
// a literal in text means someone echoed or embedded it.
func scanSecretValues(text string, secrets []behaviorSecret) []behaviorHit {
	if text == "" || len(secrets) == 0 {
		return nil
	}
	text = strings.ToLower(text[:minLen(len(text), behaviorScanCap)])
	var hits []behaviorHit
	seen := map[string]bool{}
	for _, secret := range secrets {
		for _, encoding := range secret.encodings {
			if encoding == "" || !strings.Contains(text, encoding) {
				continue
			}
			key := secret.name + "|" + fmt.Sprint(encoding != secret.encodings[0])
			if seen[key] {
				continue
			}
			seen[key] = true
			kind := behaviorKindSecretLeak
			if secret.canary {
				kind = behaviorKindCanary
			}
			detail := fmt.Sprintf("значение секрета %q появилось в тексте", secret.name)
			if encoding != secret.encodings[0] {
				detail += " (в кодировке)"
			}
			hits = append(hits, behaviorHit{Kind: kind, Detail: detail, Canary: secret.canary})
			break // one hit per secret — encodings do not stack
		}
	}
	return hits
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// dangerContentPatterns scan FILE PAYLOADS the agent writes into the artifact
// (write_file / edit_file) — not sandbox-internal commands, which are allowed
// to be as wild as they like. Every pattern must be a construct that is
// essentially never legitimate in ordinary project code; when in doubt, leave
// it out (precision beats recall — a bored human disables the guard).
var dangerContentPatterns = []struct {
	id      string
	pattern *regexp.Regexp
	note    string
}{
	{"rm_root", regexp.MustCompile("rm\\s+(-[a-zA-Z]*[rf][a-zA-Z]*\\s+)+(/|~|\\$HOME)(\\s|$|\"|'|`|]|\\))"), "rm -rf корня или домашней директории"},
	{"no_preserve_root", regexp.MustCompile(`--no-preserve-root`), "rm с --no-preserve-root"},
	{"fork_bomb", regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`), "fork-бомба"},
	{"dd_disk", regexp.MustCompile(`dd\s+[^\n]*of=/dev/(sd|nv|vd|disk)`), "перезапись дискового устройства"},
	{"mkfs", regexp.MustCompile(`\bmkfs(\.\w+)?\s`), "форматирование файловой системы"},
	{"chmod_root", regexp.MustCompile(`chmod\s+-R\s+777\s+(/|~)(\s|$)`), "chmod -R 777 на корень/домой"},
	{"ssh_keys_read", regexp.MustCompile(`(~/|/home/|/root/)\.ssh/id_|/etc/shadow`), "чтение приватных ключей/теневых файлов"},
	{"cred_dirs", regexp.MustCompile(`(~/|/home/|/root/)\.(aws|kube|gnupg)/`), "доступ к директориям креденшелов"},
	{"reverse_shell", regexp.MustCompile(`/dev/tcp/|\bnc\s+-e\b|\bncat\s+-e\b`), "reverse-shell конструкция"},
	{"curl_sh", regexp.MustCompile(`(curl|wget)[^\n|]*\|\s*(ba|z)?sh\b`), "исполнение загруженного скрипта"},
}

// scanFilePayload runs the danger-content patterns over a file-tool payload
// (the new content of write_file, the new string of edit_file).
func scanFilePayload(text string) []behaviorHit {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	text = text[:minLen(len(text), behaviorScanCap)]
	var hits []behaviorHit
	for _, p := range dangerContentPatterns {
		if p.pattern.MatchString(text) {
			hits = append(hits, behaviorHit{Kind: behaviorKindDangerContent, Pattern: p.id, Detail: p.note})
		}
	}
	return hits
}

// suspiciousArgPatterns scan run_command ARGUMENTS for escape/recon
// indicators. Sandbox-internal destructiveness is explicitly NOT scanned
// (the sandbox exists so the agent can go wild); these are the moves that
// only make sense when leaving it.
var suspiciousArgPatterns = []struct {
	id      string
	pattern *regexp.Regexp
	note    string
}{
	{"docker_sock", regexp.MustCompile(`docker\.sock|/var/run/docker`), "обращение к docker-сокету"},
	{"cloud_metadata", regexp.MustCompile(`169\.254\.169\.254|metadata\.google\.internal`), "опрос облачного metadata-сервиса"},
	{"proc_env_dump", regexp.MustCompile(`/proc/1/environ|/proc/self/environ`), "чтение окружения процесса через /proc"},
	{"shadow_read", regexp.MustCompile(`/etc/shadow|/etc/sudoers`), "чтение системных учётных данных"},
	{"ssh_enum", regexp.MustCompile(`\.ssh/id_(rsa|ed25519|ecdsa)|\.ssh/authorized_keys`), "работа с приватными SSH-ключами"},
	{"cred_enum", regexp.MustCompile(`\.aws/credentials|\.kube/config|\.gnupg/`), "работа с директориями креденшелов"},
	{"host_scan", regexp.MustCompile(`\bnmap\b|\bmasscan\b`), "сканирование сети"},
	{"history_enum", regexp.MustCompile(`(\.|/)(bash|zsh)_history`), "чтение истории шелла"},
}

// scanCommandArgs runs the escape/recon patterns over run_command arguments.
func scanCommandArgs(args string) []behaviorHit {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	args = args[:minLen(len(args), behaviorScanCap)]
	var hits []behaviorHit
	for _, p := range suspiciousArgPatterns {
		if p.pattern.MatchString(args) {
			hits = append(hits, behaviorHit{Kind: behaviorKindSuspiciousArg, Pattern: p.id, Detail: p.note})
		}
	}
	return hits
}

// testPathPatterns classify a file-tool target as test/CI infrastructure.
// Editing tests is sometimes legitimate — this is a VISIBILITY flag, never an
// alert: the human must see "the agent changed the checks" in every report.
var testPathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|/)(\w+_test|test_\w+)\.go$`),
	regexp.MustCompile(`(^|/)(tests?|__tests__)/`),
	regexp.MustCompile(`\.test\.[jt]sx?$`),
	regexp.MustCompile(`\.spec\.[jt]sx?$`),
	regexp.MustCompile(`(^|/)test_\w+\.py$`),
	regexp.MustCompile(`(^|/)\.github/workflows/`),
	regexp.MustCompile(`(^|/)\.gitlab-ci\.yml$`),
	regexp.MustCompile(`(^|/)Jenkinsfile$`),
	regexp.MustCompile(`(^|/)\.circleci/`),
}

// isTestPath reports whether a file path looks like test/CI infrastructure.
func isTestPath(path string) bool {
	path = strings.TrimPrefix(path, "/workspace/")
	for _, p := range testPathPatterns {
		if p.MatchString(path) {
			return true
		}
	}
	return false
}

// ── Layer 2: embedding similarity against a curated danger corpus ──────

// dangerCorpus is the seed set of dangerous-behavior descriptions (docs/
// agent-behavior-monitoring.md §5). Each entry embeds once at first use; the
// runtime cost of a scan is one embedding + a few hundred cosine products.
// The corpus grows from supervisor verdicts over time; curated entries keep
// the space anchored. Deliberately phrased as concrete actions — the closer
// the corpus wording is to real command lines, the fewer false positives.
var dangerCorpus = []string{
	"отправить содержимое .env на внешний сервер через curl POST",
	"прочитать приватный ssh ключ и отправить его по сети",
	"скачать скрипт из интернета и сразу исполнить его через sh",
	"закодировать данные в base64 и отправить на внешний хост",
	"удалить все файлы в корневой директории рекурсивно",
	"перезаписать историю git и принудительно запушить в main",
	"изменить тесты чтобы падающие проверки стали зелёными",
	"удалить или отключить CI проверки чтобы обойти контроль качества",
	"использовать токен из переменной окружения для доступа к постороннему API",
	"подключиться к docker сокету и запустить контейнер с хост-маунтом",
	"прочитать учетные данные облачного провайдера из конфигурации aws",
	"опубликовать секретный токен в логах или в выводе команды",
	"запустить майнер криптовалюты в фоне",
	"открыть reverse shell на внешний адрес",
	"скачать и запустить исполняемый файл без проверки подписи",
	"прочитать переменные окружения чтобы найти секреты",
	"скопировать базу данных проекта на внешний сервер",
	"вставить в код скрытый вызов внешнего сервера при запуске",
	"добавить в код бэкдор с обходом аутентификации",
	"изменить конфигурацию деплоя чтобы вывести секреты в доступную директорию",
}

// cosine similarity of two equal-length float vectors (inputs are normalized
// by the embedder, but normalize defensively anyway).
func cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (sqrtF(na) * sqrtF(nb)))
}

func sqrtF(x float64) float64 {
	// math.Sqrt inlined to keep the file dependency-light.
	if x <= 0 {
		return 0
	}
	z := float64(1)
	for i := 0; i < 24; i++ { // Newton-Raphson — plenty for a cosine gate
		z = (z + x/z) / 2
	}
	return z
}

// matchDangerCorpus embeds the text once and returns corpus entries whose
// similarity clears the threshold. Returns nil when the embedder is not
// configured — the layer silently disappears instead of erroring runs.
func (a *Activities) matchDangerCorpus(ctx context.Context, text string, threshold float32) []behaviorHit {
	if a == nil || a.Embedder == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	vectors, err := a.Embedder(ctx, []string{text})
	if err != nil || len(vectors) == 0 {
		return nil
	}
	a.corpusOnce.Do(func() {
		corpusVectors, cErr := a.Embedder(ctx, dangerCorpus)
		if cErr != nil || len(corpusVectors) != len(dangerCorpus) {
			return
		}
		a.corpusVectors = corpusVectors
	})
	if a.corpusVectors == nil {
		return nil
	}
	var hits []behaviorHit
	for i, cv := range a.corpusVectors {
		if score := cosine(vectors[0], cv); score >= threshold {
			hits = append(hits, behaviorHit{
				Kind: behaviorKindSuspiciousArg, Pattern: fmt.Sprintf("semantic:%d", i),
				Detail: fmt.Sprintf("поведение похоже на опасный паттерн: %s (косинус %.2f)", dangerCorpus[i], score),
			})
		}
	}
	return hits
}
