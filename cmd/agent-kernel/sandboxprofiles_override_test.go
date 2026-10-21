package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const apiOperatorRecipe = "FROM python:3.12-alpine\nRUN apk add --no-cache git\n"

// POST …/preview accepts an optional dockerfile_override: a valid text comes
// back verbatim; a violating one is a 422 naming the line; mixing recipe
// sources in one preview is refused.
func TestPreviewDockerfileOverride(t *testing.T) {
	store := newFakeSandboxProfileStore(builtinProfileFixture())
	server := newSandboxProfileServer(t, store, &captureProfileOutbox{}, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"dockerfile_override":"FROM python:3.12-alpine\nRUN apk add --no-cache git\n"}`)
	require.Equal(t, 200, status, body)
	var payload struct {
		Dockerfile string `json:"dockerfile"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Equal(t, apiOperatorRecipe, payload.Dockerfile)

	status, body = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"dockerfile_override":"FROM alpine:3.20\nADD x y"}`)
	require.Equal(t, 422, status, body)
	require.Contains(t, body, "строка 2")

	// D7: parser directives and RUN --network=host are 422 with their line.
	status, body = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"dockerfile_override":"# syntax=docker/dockerfile:1\nFROM alpine:3.20\n"}`)
	require.Equal(t, 422, status, body)
	require.Contains(t, body, "строка 1")
	require.Contains(t, body, "#syntax=")
	status, body = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"dockerfile_override":"FROM alpine:3.20\nRUN --network=host nmap\n"}`)
	require.Equal(t, 422, status, body)
	require.Contains(t, body, "строка 2")
	require.Contains(t, body, "--network=host")

	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/preview",
		`{"base":"python","dockerfile_override":"FROM alpine:3.20\n"}`)
	require.Equal(t, 422, status)
}

// The full operator-recipe lifecycle: create with the override, build from
// the stored text (digest pinned, event journaled), edit the text, and 422
// on every attempt to mix recipe sources — including on builtins (AC-2,
// AC-3).
func TestOverrideProfileLifecycle(t *testing.T) {
	store := newFakeSandboxProfileStore(builtinProfileFixture())
	outbox := &captureProfileOutbox{}
	server := newSandboxProfileServer(t, store, outbox, fakeProfileBuilders{})
	defer server.Close()

	status, body := profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"op-recipe","name":"OP recipe","dockerfile_override":"FROM python:3.12-alpine\nRUN apk add --no-cache git\n"}`)
	require.Equal(t, 201, status, body)

	// Mixing sources at create is an explicit 422 (AC-2).
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"mixed","name":"Mixed","base":"python","dockerfile_override":"FROM alpine:3.20\n"}`)
	require.Equal(t, 422, status)
	status, _ = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles",
		`{"id":"imgmix","name":"Img","image":"repo@sha256:`+repeatHex("ab")+`","dockerfile_override":"FROM alpine:3.20\n"}`)
	require.Equal(t, 422, status)

	// Build from the override verbatim: digest pinned, event journaled.
	status, body = profileRequest(t, server, http.MethodPost, "writertoken", "/v1/workspace/sandbox-profiles/op-recipe/build", "")
	require.Equal(t, 200, status, body)
	require.Contains(t, body, "temporality-sandbox-test@sha256:")
	built := outbox.byType("sandbox.profile.built")
	require.Len(t, built, 1)
	require.Equal(t, "op-recipe", built[0].Data["profile"])
	pinned, err := store.GetSandboxProfile(context.Background(), "op-recipe")
	require.NoError(t, err)
	require.NotEmpty(t, pinned.Image)
	require.Equal(t, apiOperatorRecipe, pinned.DockerfileOverride)

	// Editing the override text saves (and, per the store rules, resets the
	// stale digest); mixing sources on PUT is a 422.
	status, _ = profileRequest(t, server, http.MethodPut, "writertoken", "/v1/workspace/sandbox-profiles/op-recipe",
		`{"id":"op-recipe","name":"OP recipe","dockerfile_override":"FROM python:3.12-alpine\nRUN apk add --no-cache git curl\n"}`)
	require.Equal(t, 200, status)
	status, _ = profileRequest(t, server, http.MethodPut, "writertoken", "/v1/workspace/sandbox-profiles/op-recipe",
		`{"name":"x","base":"python","dockerfile_override":"FROM alpine:3.20\n"}`)
	require.Equal(t, 422, status)

	// AC-3: a builtin never accepts the override.
	status, _ = profileRequest(t, server, http.MethodPut, "writertoken", "/v1/workspace/sandbox-profiles/node",
		`{"name":"Rename","dockerfile_override":"FROM alpine:3.20\n"}`)
	require.Equal(t, 422, status)
}

func repeatHex(pair string) string {
	out := make([]byte, 0, 64)
	for i := 0; i < 32; i++ {
		out = append(out, pair...)
	}
	return string(out)
}
