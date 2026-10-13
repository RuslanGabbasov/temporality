package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

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

// buildTimeout bounds the whole docker build: pulling golang:1.27-alpine on a
// slow link is the slow part; the apk layer is small.
const buildTimeout = 15 * time.Minute

// BuildEmbedded builds the embedded Dockerfile with the local Docker daemon
// and returns the digest-pinned reference (temporality-sandbox@sha256:…).
// The build context is the Dockerfile alone, sent over stdin — no host
// directory is ever mounted or sent to the daemon.
func BuildEmbedded(ctx context.Context, binary string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	build := exec.CommandContext(ctx, binary, "build", "-t", BuildImageTag, "-")
	build.Stdin = strings.NewReader(EmbeddedDockerfile)
	var output limitedBuffer
	build.Stdout, build.Stderr = &output, &output
	if err := build.Run(); err != nil {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return "", fmt.Errorf("sandbox image build timed out after %s: %w", buildTimeout, ctxErr)
		}
		return "", fmt.Errorf("sandbox image build: %w: %s", err, output.String())
	}
	ref, err := pinnedRef(ctx, binary, BuildImageTag)
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

// pinnedRef resolves a tag to its digest-pinned reference.
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
	return "temporality-sandbox@" + digest, nil
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
