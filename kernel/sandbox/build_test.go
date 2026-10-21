package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// D7.3/F-05: the verification probe overrides the image's entrypoint — the
// image config (a daemon-like ENTRYPOINT) must never execute on the check
// step — and runs under the full sandbox isolation contour mirrored from the
// runtime flags (docker.go), because PATH resolution of "true" happens
// inside the image: the probe must not grant the image more trust than an
// ordinary sandbox run. Every docker flag must sit before the image
// reference.
func TestVerifyRunArgsOverridesEntrypoint(t *testing.T) {
	const ref = "temporality-sandbox-qa-tools@sha256:" + "ab"
	args := verifyRunArgs(ref)
	want := []string{"run", "--rm", "--pull=never",
		"--network=none", "--read-only", "--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--pids-limit", "512",
		"--user", "65534:65534",
		"--entrypoint", "true", ref}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("verifyRunArgs = %q, want %q", args, want)
	}
	imageAt := -1
	entrypointAt := -1
	for i, arg := range args {
		if arg == ref {
			imageAt = i
		}
		if arg == "--entrypoint" {
			entrypointAt = i
		}
	}
	if entrypointAt < 0 || imageAt < 0 || entrypointAt > imageAt {
		t.Fatalf("--entrypoint must come before the image ref, got %v", args)
	}
	for _, isolation := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit", "--user"} {
		if !slices.Contains(args, isolation) {
			t.Errorf("verifyRunArgs missing the isolation flag %q: %v", isolation, args)
		}
	}
}

func TestSocketErrorPermissionDenied(t *testing.T) {
	raw := "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Post \"http://%2Fvar%2Frun%2Fdocker.sock/v1.47/version\": dial unix /var/run/docker.sock: connect: permission denied"
	err := socketError(raw, errors.New("exit status 1"), 10001)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"uid=10001", "TEMPORALITY_DOCKER_GID", "docker compose up -d --build agent-kernel"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	if strings.Count(msg, "\n") != 1 {
		t.Errorf("expected a single hint line, got: %s", msg)
	}
}

func TestSocketErrorDaemonUnreachable(t *testing.T) {
	err := socketError("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", errors.New("exit status 1"), 10001)
	if err == nil || !strings.Contains(err.Error(), "not reachable") || !strings.Contains(err.Error(), "docker.sock") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSocketErrorPassthrough(t *testing.T) {
	err := socketError("some other failure", errors.New("boom"), 10001)
	if err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "TEMPORALITY_DOCKER_GID") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCustomDefinition(t *testing.T) {
	def, err := CustomDefinition("qa-tools", "python", []string{"gcc", "linux-headers"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Tag != "temporality-sandbox-qa-tools:latest" {
		t.Errorf("unexpected tag %q", def.Tag)
	}
	// The composed recipe keeps the base's CLI layer and appends the extra
	// apk line for an alpine base.
	for _, want := range []string{"FROM python:3.12-alpine", "apk add --no-cache git", "apk add --no-cache gcc linux-headers"} {
		if !strings.Contains(def.Dockerfile, want) {
			t.Errorf("dockerfile missing %q:\n%s", want, def.Dockerfile)
		}
	}

	// Debian base (browser-e2e) installs extras with apt.
	def, err = CustomDefinition("db-tests", "browser-e2e", []string{"postgresql-client"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(def.Dockerfile, "apt-get install -y --no-install-recommends postgresql-client") {
		t.Errorf("dockerfile missing apt line:\n%s", def.Dockerfile)
	}

	// The embedded minimal base is a valid base too.
	if _, err = CustomDefinition("shell-only", "base", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No packages: the recipe equals the base recipe with a fresh tag.
	plain, err := CustomDefinition("plain", "node", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	baseDef, _ := BaseDefinition("node")
	if plain.Dockerfile != baseDef.Dockerfile {
		t.Errorf("package-free recipe must equal the base recipe")
	}
}

func TestCustomDefinitionRejects(t *testing.T) {
	if _, err := CustomDefinition("Bad_ID", "python", nil); err == nil {
		t.Error("expected error for invalid profile id")
	}
	if _, err := CustomDefinition("ok-id", "invented-base", nil); err == nil {
		t.Error("expected error for unknown base")
	}
	// Shell metacharacters never reach the RUN line.
	for _, bad := range []string{"gcc; rm -rf /", "$(id)", "a b", "x`id`"} {
		if _, err := CustomDefinition("ok-id", "python", []string{bad}); err == nil {
			t.Errorf("expected error for package %q", bad)
		}
	}
	sixtyFive := make([]string, 65)
	for i := range sixtyFive {
		sixtyFive[i] = "pkg"
	}
	if _, err := CustomDefinition("ok-id", "python", sixtyFive); err == nil {
		t.Error("expected error for too many packages")
	}
	// A Debian name on an alpine base (or the reverse) fails the build minutes
	// in with a bare apk error — reject it up front and name the right package.
	if _, err := CustomDefinition("ok-id", "node", []string{"golang-go", "docker.io", "docker-compose"}); err == nil {
		t.Error("expected error for apt names on an alpine base")
	} else if !strings.Contains(err.Error(), `"go"`) {
		t.Errorf("error must name the alpine equivalent, got: %v", err)
	}
	if _, err := CustomDefinition("ok-id", "browser-e2e", []string{"build-base"}); err == nil {
		t.Error("expected error for an apk name on the debian base")
	} else if !strings.Contains(err.Error(), `"build-essential"`) {
		t.Errorf("error must name the apt equivalent, got: %v", err)
	}
	// Names that exist in both distros pass untouched.
	if _, err := CustomDefinition("ok-id", "node", []string{"go", "docker-cli", "make"}); err != nil {
		t.Errorf("alpine names must pass: %v", err)
	}
	if !ValidPackageName("libicu-dev") || !ValidPackageName("python3-dev") || !ValidPackageName("postgresql-client") {
		t.Error("valid package names rejected")
	}
	if ValidPackageName("GCC") || ValidPackageName("") || ValidPackageName("a/b") {
		t.Error("invalid package name accepted")
	}
}

// F-06: the captured docker output is untrusted (mutable/external FROM can
// emit anything from a layer) and travels into error text — it must carry no
// ANSI/VT escapes, no control bytes, no CR rewrites and no invalid UTF-8.
func TestSanitizeDockerOutputStripsANSIAndControlBytes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"CSI clear-screen and color", "\x1b[2Jerror \x1b[1;31mred\x1b[0m done", "error red done"},
		{"OSC window title via BEL", "\x1b]0;pwned title\x07ERROR: build failed", "ERROR: build failed"},
		{"OSC window title via ST", "\x1b]2;title\x1b\\next line", "next line"},
		{"DCS string sequence", "\x1bPpayload\x1b\\kept", "kept"},
		{"single-character ESC forms", "\x1b7keep\x1bMtail", "keeptail"},
		{"charset designation", "before\x1b(Bafter", "beforeafter"},
		{"dangling ESC at the end", "before\x1b", "before"},
		{"C0 controls except newline and tab", "a\x00b\x07c\x08d\x0be\x0cf\x10g", "abcdefg"},
		{"tab and newline survive", "line1\tcol\nline2", "line1\tcol\nline2"},
		{"CRLF becomes LF", "a\r\nb\r\n", "a\nb\n"},
		{"lone CR becomes LF (no line overwrite)", "forged\roverwrite", "forged\noverwrite"},
		{"DEL", "to\x7fdel", "todel"},
		{"C1 controls", "x\u0080y\u009e\u009fz", "xyz"},
		{"invalid UTF-8 bytes dropped", "ok \xff\xfe tail", "ok  tail"},
		{"printable text untouched", "plain ascii, русский текст, emoji 😀", "plain ascii, русский текст, emoji 😀"},
	}
	for _, tc := range cases {
		got := sanitizeDockerOutput(tc.in)
		if got != tc.want {
			t.Errorf("%s: sanitizeDockerOutput(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: result is not valid UTF-8: %q", tc.name, got)
		}
	}
}

// The size cap keeps the tail — the build failure summary lives at the end
// of the log — with a truncation marker up front, cutting on a rune boundary.
func TestSanitizeDockerOutputKeepsTailWithMarker(t *testing.T) {
	suffix := " итог сборки: sqlmap (no such package)"
	in := strings.Repeat("noisy build line\n", 5000) + strings.Repeat("ы", 9000) + suffix
	got := sanitizeDockerOutput(in)
	if len(got) > dockerOutputLimit {
		t.Fatalf("result is %d bytes, cap is %d", len(got), dockerOutputLimit)
	}
	if !strings.HasPrefix(got, outputTruncationMarker) {
		t.Errorf("missing truncation marker at the start: %q", got[:64])
	}
	if !strings.HasSuffix(got, suffix) {
		t.Errorf("tail with the failure summary must be kept: %q", got[len(got)-64:])
	}
	if strings.Contains(got, "noisy build line") {
		t.Error("the head of an oversized log must be cut")
	}
	if !utf8.ValidString(got) {
		t.Error("truncation split a rune — result is not valid UTF-8")
	}
	// Under the cap the text passes through untouched.
	if under := sanitizeDockerOutput("short log\n"); under != "short log\n" {
		t.Errorf("short log must stay untouched, got %q", under)
	}
}

// fakeDocker installs a scripted docker CLI stand-in: each subcommand is
// answered by the first argv element.
func fakeDocker(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// F-06 end-to-end: a hostile build log (CSI/OSC escapes, CR rewrites, a NUL
// byte) reaches the error message only in sanitized form — the plain failure
// path must keep the readable lines and drop every control byte.
func TestBuildDefinitionHostileLogIsSanitized(t *testing.T) {
	binary := fakeDocker(t, `case "$1" in
version) echo "Docker version 9.9.9";;
build) printf '%b' 'BEFORE\033[2J\033]0;pwned title\007MIDDLE\rFORGED\rTAIL\000end\n'; exit 1;;
esac
exit 0
`)
	def, err := CustomDefinition("qa-tools", "python", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildDefinition(context.Background(), binary, def)
	if err == nil {
		t.Fatal("expected the build to fail")
	}
	msg := err.Error()
	for _, want := range []string{"sandbox image build", "BEFOREMIDDLE\nFORGED\nTAILend\n"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
	for _, banned := range []string{"\x1b", "\r", "\x00", "[2J", "pwned title"} {
		if strings.Contains(msg, banned) {
			t.Errorf("error carries unsanitized bytes %q:\n%s", banned, msg)
		}
	}
}

// Regression: sanitization happens before classification, so the apk/apt
// «no such package» mapping and the apt↔apk alias hint keep working — a
// hostile wrapper around the refusal changes nothing.
func TestBuildDefinitionSanitizedLogKeepsPackageClassification(t *testing.T) {
	apkBinary := fakeDocker(t, `case "$1" in
version) echo "Docker version 9.9.9";;
build) printf '%b' '\033[2J#6 0.725 ERROR: unable to select packages:\r\n#6 0.725 sqlmap (no such package):\r\n\033[0m'; exit 1;;
esac
exit 0
`)
	def, err := CustomDefinition("qa-tools", "python", []string{"sqlmap"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildDefinition(context.Background(), apkBinary, def)
	if err == nil {
		t.Fatal("expected the build to fail")
	}
	msg := err.Error()
	for _, want := range []string{`пакет "sqlmap"`, "не найден в репозиториях apk", "python:3.12-alpine", "проверьте имя пакета", "sqlmap (no such package)", "--- docker build log ---"} {
		if !strings.Contains(msg, want) {
			t.Errorf("classification missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "\x1b") || strings.Contains(msg, "\r") {
		t.Errorf("classified error carries unsanitized bytes:\n%s", msg)
	}

	aliasBinary := fakeDocker(t, `case "$1" in
version) echo "Docker version 9.9.9";;
build) printf '%b' '\rERROR: unable to select packages:\ngolang-go (no such package):\n'; exit 1;;
esac
exit 0
`)
	aliasDef, err := CustomDefinition("qa-tools", "python", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildDefinition(context.Background(), aliasBinary, aliasDef)
	if err == nil {
		t.Fatal("expected the build to fail")
	}
	for _, want := range []string{`"go"`, "имя другой пакетной системы", "не найден в репозиториях apk"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("alias hint missing %q:\n%s", want, err)
		}
	}
	if strings.Contains(err.Error(), "\r") {
		t.Errorf("alias error carries raw CR:\n%s", err)
	}
}

// The other build.go paths that embed docker output into errors — image
// pull, verify-probe and the preflight socket probe — are sanitized the same
// way (F-06: no unsanitized docker byte reaches error text).
func TestPullImageAndVerifyErrorsAreSanitized(t *testing.T) {
	binary := fakeDocker(t, `case "$1" in
version) echo "Docker version 9.9.9";;
build) exit 0;;
image) printf 'sha256:`+strings.Repeat("a", 64)+`\n';;
pull) printf '%b' 'PULL\033[31mred\033[0m\rFORGED-PULL\n'; exit 1;;
run) printf '%b' 'VERIFY\033[2J\rFORGED-VERIFY\n'; exit 1;;
esac
exit 0
`)
	ctx := context.Background()

	_, err := PullImage(ctx, binary, "evil/image:latest")
	if err == nil {
		t.Fatal("expected the pull to fail")
	}
	msg := err.Error()
	for _, want := range []string{"image pull", "PULLred\nFORGED-PULL\n"} {
		if !strings.Contains(msg, want) {
			t.Errorf("pull error missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "\x1b") || strings.Contains(msg, "\r") {
		t.Errorf("pull error carries unsanitized bytes:\n%s", msg)
	}

	def, err := CustomDefinition("qa-tools", "python", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildDefinition(ctx, binary, def)
	if err == nil {
		t.Fatal("expected the verify probe to fail")
	}
	msg = err.Error()
	for _, want := range []string{"verify built sandbox image", "VERIFY\nFORGED-VERIFY\n"} {
		if !strings.Contains(msg, want) {
			t.Errorf("verify error missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "\x1b") || strings.Contains(msg, "\r") {
		t.Errorf("verify error carries unsanitized bytes:\n%s", msg)
	}
}

func TestPreflightSocketErrorOutputIsSanitized(t *testing.T) {
	binary := fakeDocker(t, `case "$1" in
version) printf '%b' 'PRO\033[2J\033]0;x\007BE\rforged\n'; exit 1;;
esac
exit 0
`)
	err := PreflightSocket(context.Background(), binary)
	if err == nil {
		t.Fatal("expected the probe to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "docker daemon probe failed") || !strings.Contains(msg, "PROBE") {
		t.Errorf("probe error missing its first line:\n%s", msg)
	}
	if strings.Contains(msg, "\x1b") || strings.Contains(msg, "\r") {
		t.Errorf("probe error carries unsanitized bytes:\n%s", msg)
	}
}
