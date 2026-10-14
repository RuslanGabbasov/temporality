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
