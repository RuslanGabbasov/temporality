package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// sandboxProfileIDPattern mirrors workspace's profile id shape: lowercase
// latin/digits/dash, 2-32 chars — the same pattern travels into local build
// tags, which must be valid docker references.
var sandboxProfileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// EmbeddedDockerfile mirrors sandbox/Dockerfile (kernel/sandbox/embedded_test.go
// fails when the two drift). The kernel image does not carry the sandbox/
// directory in its build context, so the Dockerfile travels as this constant:
// first-run onboarding builds the sandbox image from the UI through
// POST /v1/setup/sandbox/build without touching the repository checkout.
const EmbeddedDockerfile = `FROM golang:1.27-alpine
# curl: agents fetch URLs without falling back to hand-written python
# scripts; jq: idiomatic JSON handling for HTTP API responses.
# bash: model-authored heredocs and scripts assume a POSIX-plus shell — plain
# ash breaks them in subtle ways.
# python3: the scripting runtime agents reach for most; its absence forced
# byte-by-byte file surgery via sh.
# ripgrep: fast code search without fragile grep -r invocations.
RUN apk add --no-cache git openssh-client ca-certificates curl jq bash python3 ripgrep
`

// BuildImageTag is the tag the runtime build lands on before pinning.
const BuildImageTag = "temporality-sandbox:latest"

// ProfileDefinition is the build recipe of one builtin sandbox profile
// (docs/sandbox-profiles.md §5). Dockerfiles live in code — the kernel image
// does not carry the sandbox/ directory in its build context.
type ProfileDefinition struct {
	Profile    string
	Tag        string
	Dockerfile string
}

// ProfileDefinitions maps builtin profile ids to their build recipes. The
// id set mirrors workspace.BuiltinSandboxProfiles; a cmd-level test keeps
// the two catalogs from drifting apart.
func ProfileDefinitions() map[string]ProfileDefinition {
	return map[string]ProfileDefinition{
		"node": {
			Profile: "node",
			Tag:     "temporality-sandbox-node:latest",
			Dockerfile: `FROM node:22-alpine
` + apkTools,
		},
		"python": {
			Profile: "python",
			Tag:     "temporality-sandbox-python:latest",
			Dockerfile: `FROM python:3.12-alpine
` + apkTools,
		},
		"browser-e2e": {
			Profile: "browser-e2e",
			Tag:     "temporality-sandbox-browser-e2e:latest",
			// The official Playwright image ships Node and the matching browser
			// build — assembling the pair by hand (alpine + apk chromium) breaks on
			// every Playwright upgrade.
			Dockerfile: `FROM mcr.microsoft.com/playwright:v1.49.0-jammy
` + aptTools,
		},
	}
}

// apkTools is the shared CLI layer over alpine bases: the base image's own
// comment block (sandbox/Dockerfile) explains each package.
const apkTools = `# curl: agents fetch URLs without falling back to hand-written python
# scripts; jq: idiomatic JSON handling for HTTP API responses.
# bash: model-authored heredocs and scripts assume a POSIX-plus shell — plain
# ash breaks them in subtle ways.
# ripgrep: fast code search without fragile grep -r invocations.
RUN apk add --no-cache git openssh-client ca-certificates curl jq bash ripgrep
`

// aptTools is the same CLI layer for debian-based bases (browser profile).
const aptTools = `# Same CLI layer as the alpine profiles on a debian base.
RUN apt-get update && apt-get install -y --no-install-recommends git openssh-client ca-certificates curl jq bash ripgrep && rm -rf /var/lib/apt/lists/*
`

// buildTimeout bounds the whole docker build: pulling golang:1.27-alpine on a
// slow link is the slow part; the apk layer is small.
const buildTimeout = 15 * time.Minute

// BaseDefinition returns the build recipe of a known base: the three builtin
// profiles plus "base" — the embedded minimal alpine recipe. Custom profiles
// compose on one of these; they never invent a base image.
func BaseDefinition(base string) (ProfileDefinition, bool) {
	if base == "base" {
		return ProfileDefinition{Profile: "base", Tag: BuildImageTag, Dockerfile: EmbeddedDockerfile}, true
	}
	def, ok := ProfileDefinitions()[base]
	return def, ok
}

// validPackageName allows only characters that appear in apk/apt package
// names (lowercase latin, digits, dot, plus, dash, underscore). Names are
// interpolated into the recipe's RUN line, so the allowlist doubles as shell
// injection protection.
var validPackageName = regexp.MustCompile(`^[a-z0-9][a-z0-9.+_-]*$`)

// ValidPackageName reports whether name is a safe OS package name.
func ValidPackageName(name string) bool { return validPackageName.MatchString(name) }

// maxRecipePackages bounds one recipe's extra package list; a sane Dockerfile
// layer never needs more.
const maxRecipePackages = 64

// aptToApkNames maps the Debian/Ubuntu package names models and developers
// reach for by habit onto their alpine equivalents: the apk index has no such
// names, and the build would waste minutes pulling layers before dying with
// a bare «no such package». The custom-profile validator turns every hit
// into an actionable error before any docker call.
var aptToApkNames = map[string]string{
	"golang-go":       "go",
	"docker.io":       "docker-cli",
	"docker-compose":  "docker-cli-compose",
	"build-essential": "build-base",
	"python3-pip":     "py3-pip",
	"python3-venv":    "python3",
	"libssl-dev":      "openssl-dev",
	"libpq-dev":       "postgresql-dev",
	"pkg-config":      "pkgconf",
}

// apkToAptNames is the reverse check for the debian-based browser-e2e base.
var apkToAptNames = map[string]string{
	"go":                 "golang-go",
	"docker-cli":         "docker.io",
	"docker-cli-compose": "docker-compose",
	"build-base":         "build-essential",
	"py3-pip":            "python3-pip",
	"openssl-dev":        "libssl-dev",
	"postgresql-dev":     "libpq-dev",
	"pkgconf":            "pkg-config",
}

// packageManagerMismatch reports the correct name when package is a valid
// name of the OTHER distro: the builtin bases are alpine (apk) except
// browser-e2e (apt), and Debian names are written far more often than alpine
// ones.
func packageManagerMismatch(base, name string) (string, bool) {
	if base == "browser-e2e" {
		if apt, ok := apkToAptNames[name]; ok {
			return apt, true
		}
		return "", false
	}
	if apk, ok := aptToApkNames[name]; ok {
		return apk, true
	}
	return "", false
}

// CustomDefinition composes the build recipe for a custom profile: the base
// recipe plus one extra package-install layer using the base distro's package
// manager. profileID names the local tag so rebuilds overwrite in place.
func CustomDefinition(profileID, base string, packages []string) (ProfileDefinition, error) {
	if !sandboxProfileIDPattern.MatchString(profileID) {
		return ProfileDefinition{}, fmt.Errorf("profile id %q does not match %s", profileID, sandboxProfileIDPattern.String())
	}
	baseDef, ok := BaseDefinition(base)
	if !ok {
		return ProfileDefinition{}, fmt.Errorf("unknown sandbox base %q", base)
	}
	if len(packages) > maxRecipePackages {
		return ProfileDefinition{}, fmt.Errorf("too many packages: %d > %d", len(packages), maxRecipePackages)
	}
	for _, name := range packages {
		if len(name) > 64 || !ValidPackageName(name) {
			return ProfileDefinition{}, fmt.Errorf("package name %q is not a valid apk/apt package", name)
		}
		if fix, mismatch := packageManagerMismatch(base, name); mismatch {
			distro, manager := "alpine", "apk"
			wrong := "Debian (apt)"
			if base == "browser-e2e" {
				distro, manager = "debian", "apt"
				wrong = "alpine (apk)"
			}
			return ProfileDefinition{}, fmt.Errorf("package %q is an %s name and does not exist in %s repositories; the %q base installs with %s — use %q instead", name, wrong, distro, base, manager, fix)
		}
	}
	dockerfile := baseDef.Dockerfile
	if len(packages) > 0 {
		dockerfile += "\n# Extra packages requested by the custom profile.\n" + packageInstallLine(base, packages)
	}
	return ProfileDefinition{
		Profile:    profileID,
		Tag:        "temporality-sandbox-" + profileID + ":latest",
		Dockerfile: dockerfile,
	}, nil
}

// packageInstallLine renders the RUN layer for extra packages. The package
// manager follows the base image's distro: apk for alpine bases, apt for the
// debian browser-e2e image (mirroring the shared CLI layer above).
func packageInstallLine(base string, packages []string) string {
	list := strings.Join(packages, " ")
	switch base {
	case "browser-e2e":
		return "RUN apt-get update && apt-get install -y --no-install-recommends " + list + " && rm -rf /var/lib/apt/lists/*"
	default: // alpine bases: base, node, python
		return "RUN apk add --no-cache " + list
	}
}

// PreflightSocket proves the daemon socket is usable by the current kernel
// user before a build or run is attempted, and turns the two deployment-level
// failures into instructions an operator can follow straight from the UI:
// a plain Linux host mounts the socket as root:docker, and the kernel's
// non-root user needs the host docker group GID added to the container.
func PreflightSocket(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, binary, "version", "--format", "{{.Server.Version}}")
	var output limitedBuffer
	probe.Stdout, probe.Stderr = &output, &output
	if err := probe.Run(); err != nil {
		return socketError(strings.TrimSpace(output.String()), err, os.Getuid())
	}
	return nil
}

// socketError classifies a failed daemon probe. Testable pure helper.
func socketError(output string, err error, uid int) error {
	first := output
	if idx := strings.IndexAny(output, "\r\n"); idx >= 0 {
		first = output[:idx]
	}
	switch {
	case strings.Contains(output, "permission denied"):
		return fmt.Errorf("docker daemon socket is not accessible for the kernel container user (uid=%d): %s\n"+
			"The entrypoint normally attaches the socket's group automatically — recreate the container with the current image: docker compose up -d --build agent-kernel. "+
			"If it persists, the socket is likely not group-accessible (ls -l /var/run/docker.sock should show srw-rw---- with a group, not 0600), or set TEMPORALITY_DOCKER_GID in .env to that group's GID", uid, first)
	case strings.Contains(output, "Cannot connect to the Docker daemon"), strings.Contains(output, "Is the docker daemon running?"):
		return fmt.Errorf("docker daemon is not reachable from the kernel container: %s\n"+
			"Check that /var/run/docker.sock is mounted into the agent-kernel service", first)
	default:
		return fmt.Errorf("docker daemon probe failed: %w: %s", err, first)
	}
}

// BuildEmbedded builds the embedded Dockerfile with the local Docker daemon
// and returns the digest-pinned reference (temporality-sandbox@sha256:…).
// The build context is the Dockerfile alone, sent over stdin — no host
// directory is ever mounted or sent to the daemon.
func BuildEmbedded(ctx context.Context, binary string) (string, error) {
	return BuildDefinition(ctx, binary, ProfileDefinition{Profile: "base", Tag: BuildImageTag, Dockerfile: EmbeddedDockerfile})
}

// BuildDefinition builds one profile recipe: same preflight, stdin-only
// context, digest pinning and --pull=never verification as the base build.
func BuildDefinition(ctx context.Context, binary string, def ProfileDefinition) (string, error) {
	if strings.TrimSpace(def.Dockerfile) == "" || strings.TrimSpace(def.Tag) == "" {
		return "", fmt.Errorf("sandbox profile definition is incomplete")
	}
	if err := PreflightSocket(ctx, binary); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	build := exec.CommandContext(ctx, binary, "build", "-t", def.Tag, "-")
	build.Stdin = strings.NewReader(def.Dockerfile)
	var output limitedBuffer
	build.Stdout, build.Stderr = &output, &output
	if err := build.Run(); err != nil {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return "", fmt.Errorf("sandbox image build timed out after %s: %w", buildTimeout, ctxErr)
		}
		return "", fmt.Errorf("sandbox image build: %w: %s", err, output.String())
	}
	ref, err := pinnedRef(ctx, binary, def.Tag)
	if err != nil {
		return "", err
	}
	if err := verifyPinned(ctx, binary, ref); err != nil {
		return "", err
	}
	return ref, nil
}

// ImageAvailable reports whether the local Docker store holds the exact
// digest-pinned image (--pull=never runs require it).
func ImageAvailable(ctx context.Context, binary, ref string) bool {
	inspect := exec.CommandContext(ctx, binary, "image", "inspect", ref)
	return inspect.Run() == nil
}

// PullImage pulls an external image reference, resolves it to the local
// digest (repo@sha256:…) and verifies it runs exactly the way the kernel
// will (--pull=never). A mutable tag reference is accepted on pull — the
// point is to pin whatever the tag resolves to today — but the returned
// reference is always digest-pinned, so runs stay reproducible until the
// next explicit pull.
func PullImage(ctx context.Context, binary, reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", fmt.Errorf("image reference is required")
	}
	if err := PreflightSocket(ctx, binary); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	pull := exec.CommandContext(ctx, binary, "pull", reference)
	var output limitedBuffer
	pull.Stdout, pull.Stderr = &output, &output
	if err := pull.Run(); err != nil {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return "", fmt.Errorf("image pull timed out after %s: %w", buildTimeout, ctxErr)
		}
		return "", fmt.Errorf("image pull: %w: %s", err, output.String())
	}
	// A tag reference needs the local tag→digest resolution; a digest-pinned
	// input is already its own answer but still gets verified below.
	ref := reference
	if !strings.Contains(ref, "@sha256:") {
		pinned, err := pinnedRef(ctx, binary, ref)
		if err != nil {
			return "", err
		}
		ref = pinned
	}
	if err := verifyPinned(ctx, binary, ref); err != nil {
		return "", err
	}
	return ref, nil
}

// pinnedRef resolves a tag to its digest-pinned reference. The repository
// name is derived from the tag (temporality-sandbox-node:latest →
// temporality-sandbox-node), so profile builds pin under their own repo.
func pinnedRef(ctx context.Context, binary, tag string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	inspect := exec.CommandContext(ctx, binary, "image", "inspect", "--format", "{{.Id}}", tag)
	inspect.Stdout = &out
	if err := inspect.Run(); err != nil {
		return "", fmt.Errorf("inspect built sandbox image: %w", err)
	}
	digest := strings.TrimSpace(out.String())
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return "", fmt.Errorf("unexpected image id %q for %s", digest, tag)
	}
	repo := tag
	if idx := strings.IndexByte(tag, ':'); idx > 0 {
		repo = tag[:idx]
	}
	return repo + "@" + digest, nil
}

// verifyPinned proves the digest reference resolves in the local store by
// running it exactly the way the kernel will (--pull=never).
func verifyPinned(ctx context.Context, binary, ref string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, binary, "run", "--rm", "--pull=never", ref, "true")
	var output limitedBuffer
	run.Stdout, run.Stderr = &output, &output
	if err := run.Run(); err != nil {
		return fmt.Errorf("verify built sandbox image %s: %w: %s", ref, err, output.String())
	}
	return nil
}
