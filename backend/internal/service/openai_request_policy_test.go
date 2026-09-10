package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestPolicyFreezesSettingsForRetry(t *testing.T) {
	settings := NewSettingService(&openAIUUIDv7RuntimeRepo{}, nil)
	policy := openai.DefaultRequestPolicy()
	policy.CodexResidencyUS = false
	settings.PublishOpenAIRequestPolicy(policy)
	ctx := FreezeOpenAIRequestPolicy(context.Background(), settings)
	settings.PublishOpenAIRequestPolicy(openai.DefaultRequestPolicy())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid", nil)
	request.Header.Set(openai.CodexResidencyHeaderName, "eu")
	request = ApplyOpenAIRequestPolicy(request, settings)
	require.Equal(t, "eu", request.Header.Get(openai.CodexResidencyHeaderName))
	next, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", nil)
	next = ApplyOpenAIRequestPolicy(next, settings)
	require.Equal(t, "us", next.Header.Get(openai.CodexResidencyHeaderName))
}

func TestOpenAIPATRequestPolicyUsesServiceSetting(t *testing.T) {
	requests := make(chan http.Header, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	original := openAICodexPATWhoamiURL
	openAICodexPATWhoamiURL = server.URL
	t.Cleanup(func() { openAICodexPATWhoamiURL = original })
	settings := NewSettingService(&openAIUUIDv7RuntimeRepo{}, nil)
	svc := NewOpenAIOAuthService(nil, nil)
	defer svc.Stop()
	svc.SetRequestPolicySettingService(settings)
	for _, enabled := range []bool{false, true} {
		policy := openai.DefaultRequestPolicy()
		policy.CodexResidencyUS = enabled
		settings.PublishOpenAIRequestPolicy(policy)
		_, err := svc.ValidateCodexPersonalAccessToken(context.Background(), "at-test", "")
		require.Error(t, err)
		want := ""
		if enabled {
			want = "us"
		}
		require.Equal(t, want, (<-requests).Get(openai.CodexResidencyHeaderName))
	}
}

func TestOpenAIAgentRegistrationRequestPolicy(t *testing.T) {
	requests := make(chan http.Header, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		_, _ = io.WriteString(w, `{"task_id":"task-test"}`)
	}))
	defer server.Close()
	original := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = original })
	_, encoded := newTestAgentIdentityKey(t)
	account := &Account{Credentials: map[string]any{"agent_runtime_id": "runtime-test", "agent_private_key": encoded}}
	for _, enabled := range []bool{false, true} {
		policy := openai.DefaultRequestPolicy()
		policy.CodexResidencyUS = enabled
		task, err := registerAgentIdentityTask(openai.WithRequestPolicy(context.Background(), policy), account)
		require.NoError(t, err)
		require.Equal(t, "task-test", task)
		want := ""
		if enabled {
			want = "us"
		}
		require.Equal(t, want, (<-requests).Get(openai.CodexResidencyHeaderName))
	}
}

func TestOpenAIModerationRequestPolicy(t *testing.T) {
	requests := make(chan http.Header, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"flagged":false}]}`)
	}))
	defer server.Close()
	settings := NewSettingService(&openAIUUIDv7RuntimeRepo{}, nil)
	svc := &ContentModerationService{httpClient: server.Client()}
	svc.SetRequestPolicySettingService(settings)
	cfg := &ContentModerationConfig{BaseURL: server.URL, TimeoutMS: 1000, Model: "omni-moderation-latest"}
	for _, enabled := range []bool{false, true} {
		policy := openai.DefaultRequestPolicy()
		policy.CodexResidencyUS = enabled
		settings.PublishOpenAIRequestPolicy(policy)
		_, err := svc.callModerationOnceWithInput(context.Background(), cfg, "test-key", "test", nil)
		require.NoError(t, err)
		want := ""
		if enabled {
			want = "us"
		}
		require.Equal(t, want, (<-requests).Get(openai.CodexResidencyHeaderName))
	}
}
