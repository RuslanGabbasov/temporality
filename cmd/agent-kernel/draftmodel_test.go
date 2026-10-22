package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

type stubProviderLister struct {
	providers []workspace.Provider
	err       error
}

func (s stubProviderLister) ListProviders(context.Context) ([]workspace.Provider, error) {
	return s.providers, s.err
}

func TestWorkspaceDraftModelPicksDeterministicProvider(t *testing.T) {
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "b", BaseURL: "http://b/v1", Models: []string{}},
		{ID: "z", BaseURL: "http://z/v1", Models: []string{"zm1", "zm0"}},
		{ID: "a", BaseURL: "http://a/v1", Models: []string{"am1"}},
	}}
	client, err := workspaceDraftModel(context.Background(), lister, func(_ context.Context, ref string) (string, error) {
		require.Equal(t, "", ref)
		return "literal", nil
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Equal(t, "am1", client.Model())
}

func TestWorkspaceDraftModelCredentialErrorPropagates(t *testing.T) {
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "a", BaseURL: "http://a/v1", APIKeyRef: "secret://missing", Models: []string{"am1"}},
	}}
	_, err := workspaceDraftModel(context.Background(), lister, func(_ context.Context, ref string) (string, error) {
		return "", errors.New("secret not found")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `provider "a" credential`)
}

func TestWorkspaceDraftModelNoProviders(t *testing.T) {
	_, err := workspaceDraftModel(context.Background(), stubProviderLister{}, func(_ context.Context, _ string) (string, error) { return "", nil })
	require.Error(t, err)
	require.Contains(t, err.Error(), "no model configured for the agent wizard")
}

// The env-configured timeout policy must survive the fallback: llm.New with
// a zero Config would use defaults instead of the operator's knobs.
func TestWorkspaceDraftModelKeepsEnvDefaults(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "http://env/v1")
	t.Setenv("TEMPORALITY_MODEL_ID", "envmodel")
	t.Setenv("TEMPORALITY_MODEL_TIMEOUT", "123s")
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "a", BaseURL: "http://a/v1", Models: []string{"am1"}},
	}}
	config, err := llm.ConfigFromEnv()
	require.NoError(t, err)
	require.Equal(t, 123*time.Second, config.Timeout)
	_, err = workspaceDraftModel(context.Background(), lister, func(_ context.Context, _ string) (string, error) { return "", nil })
	require.NoError(t, err)
}

// Regression: on installs without an env endpoint (pure onboarding) the
// wizard must still build a config with the shared defaults. The old fallback
// replaced ConfigFromEnv's error with a zero Config and sent max_tokens=0,
// which providers reject with 400.
func TestWorkspaceDraftModelWithoutEnvEndpointKeepsDefaults(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "")
	t.Setenv("TEMPORALITY_MODEL_ID", "")
	config, err := llm.OptionalConfigFromEnv()
	require.NoError(t, err)
	require.Equal(t, 16384, config.MaxOutputTokens)
	require.Equal(t, 180*time.Second, config.Timeout)
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "a", BaseURL: "http://a/v1", Models: []string{"am1"}},
	}}
	_, err = workspaceDraftModel(context.Background(), lister, func(_ context.Context, _ string) (string, error) { return "", nil })
	require.NoError(t, err)
}

// A malformed optional env value must fail loudly instead of silently
// degrading to a zero config (the old code swallowed the error entirely).
func TestWorkspaceDraftModelMalformedEnvFails(t *testing.T) {
	t.Setenv("TEMPORALITY_MODEL_TIMEOUT", "not-a-duration")
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "a", BaseURL: "http://a/v1", Models: []string{"am1"}},
	}}
	_, err := workspaceDraftModel(context.Background(), lister, func(_ context.Context, _ string) (string, error) { return "", nil })
	require.Error(t, err)
	require.Contains(t, err.Error(), `provider "a" model config`)
}

// Regression: catalogs are alphabetical (normalizeModelIDs), so "first
// model" is luck on gateways that also serve non-chat families — xiaomi
// lists MiMo-ASR before MiMo-VL, and a draft prompt sent to an ASR model
// fails at the provider.
func TestWorkspaceDraftModelSkipsNonChatModels(t *testing.T) {
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "a", BaseURL: "http://a/v1", Models: []string{"MiMo-ASR", "MiMo-Audio-7B", "text-embedding-3-small", "MiMo-VL-7B"}},
	}}
	client, err := workspaceDraftModel(context.Background(), lister, func(_ context.Context, _ string) (string, error) { return "", nil })
	require.NoError(t, err)
	require.Equal(t, "MiMo-VL-7B", client.Model())
}

// A provider whose whole catalog is non-chat must be skipped in favor of the
// next one, not hand the wizard a model that cannot answer.
func TestWorkspaceDraftModelSkipsProviderWithoutChatModels(t *testing.T) {
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "a", BaseURL: "http://a/v1", Models: []string{"whisper-1", "text-embedding-3-small"}},
		{ID: "b", BaseURL: "http://b/v1", Models: []string{"bm1"}},
	}}
	client, err := workspaceDraftModel(context.Background(), lister, func(_ context.Context, _ string) (string, error) { return "", nil })
	require.NoError(t, err)
	require.Equal(t, "bm1", client.Model())
}

// A catalog where nothing looks chat-capable keeps the historic pick (first
// provider, first model): the heuristic is advisory, and the provider's own
// error on a wrong model is the operator's signal.
func TestWorkspaceDraftModelFallsBackWhenNothingLooksChat(t *testing.T) {
	lister := stubProviderLister{providers: []workspace.Provider{
		{ID: "a", BaseURL: "http://a/v1", Models: []string{"whisper-1"}},
	}}
	client, err := workspaceDraftModel(context.Background(), lister, func(_ context.Context, _ string) (string, error) { return "", nil })
	require.NoError(t, err)
	require.Equal(t, "whisper-1", client.Model())
}

func TestLooksLikeNonChatModel(t *testing.T) {
	for _, id := range []string{
		"MiMo-ASR", "whisper-large-v3", "text-embedding-3-small", "bge-reranker-v2-m3",
		"omni-moderation-latest", "llama-guard-3", "flux.1-dev", "dall-e-3",
		"stable-diffusion-xl", "gpt-4o-audio-preview", "sora-2", "qwen-image",
	} {
		require.True(t, looksLikeNonChatModel(id), id)
	}
	for _, id := range []string{
		"glm-4.6", "MiMo-VL-7B", "gpt-4o", "qwen3-32b", "deepseek-chat",
		"claude-sonnet-4", "llama-3.3-70b-instruct",
	} {
		require.False(t, looksLikeNonChatModel(id), id)
	}
}
