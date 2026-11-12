package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDecodeCodexEngineModelIDs(t *testing.T) {
	models, err := decodeCodexEngineModelIDs([]byte(`{"object":"list","data":[{"id":" z "},{"id":"5.6 Cyber","description":{"nested":"ignored"}},{"id":"Z"},{"id":"z"},{"id":"my-alias"}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"5.6 Cyber", "Z", "my-alias", "z"}, models)
	models, err = decodeCodexEngineModelIDs([]byte(`{"object":"list","data":[]}`))
	require.NoError(t, err)
	require.Empty(t, models)
	for _, body := range []string{
		`{}`, `{"object":"list"}`, `{"object":"list","data":null}`,
		`{"object":"other","data":[]}`, `{"object":"list","data":{}}`,
		`{"object":"list","data":[{}]}`, `{"object":"list","data":[null]}`,
		`{"object":"list","data":[{"id":null}]}`, `{"object":"list","data":[{"id":3}]}`,
		`{"object":"list","data":[{"id":""}]}`, `{"object":"list","data":[{"id":"gpt-*"}]}`,
		`{"object":"list","data":[{"id":"a\nb"}]}`, `{"object":"list","data":[]}{}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, err := decodeCodexEngineModelIDs([]byte(body))
			require.Error(t, err)
		})
	}
}

func TestCodexEngineModelsSyncRequestAndEmpty(t *testing.T) {
	account := capacitySyncTestAccount(42)
	account.Schedulable = false
	proxyID := int64(9)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example", Port: 8080}
	repo := &capacitySyncTestRepo{}
	upstream := &capacitySyncTestUpstream{do: func(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, "https://engine.example/prefix/v1/models", req.URL.String())
		require.Equal(t, "Bearer test-engine-key", req.Header.Get("Authorization"))
		require.Equal(t, "application/json", req.Header.Get("Accept"))
		require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
		require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(req.Context()))
		require.Equal(t, "http://proxy.example:8080", proxy)
		require.EqualValues(t, 42, id)
		require.Equal(t, 50, concurrency)
		return capacitySyncTestResponse(200, `{"object":"list","data":[]}`), nil
	}}
	svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
	require.NoError(t, svc.syncAccountModels(t.Context(), &account))
	require.Len(t, repo.models, 1)
	require.Empty(t, repo.models[0])
	require.False(t, account.Schedulable)
	require.NotContains(t, account.Credentials, "model_mapping", "shared snapshot is immutable")
}

func TestCodexEngineModelsSyncFailureRetainsLastValue(t *testing.T) {
	for _, name := range []string{"network", "404", "degraded", "oversize", "invalid", "nil", "nil_body", "timeout", "canceled", "proxy"} {
		t.Run(name, func(t *testing.T) {
			account := capacitySyncTestAccount(1)
			repo := &capacitySyncTestRepo{models: [][]string{{"last-success"}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			upstream := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				switch name {
				case "network":
					return nil, errors.New("private credentials")
				case "404":
					return capacitySyncTestResponse(404, "{}"), nil
				case "degraded":
					response := capacitySyncTestResponse(200, `{"object":"list","data":[]}`)
					response.Header.Set("X-Codex-Catalog-Degraded", " true ")
					return response, nil
				case "oversize":
					return capacitySyncTestResponse(200, strings.Repeat(" ", 257)), nil
				case "invalid":
					return capacitySyncTestResponse(200, "{}"), nil
				case "nil":
					return nil, nil
				case "nil_body":
					return &http.Response{StatusCode: 200}, nil
				case "timeout":
					<-req.Context().Done()
					return nil, req.Context().Err()
				case "canceled":
					cancel()
				}
				return capacitySyncTestResponse(200, `{"object":"list","data":[]}`), nil
			}}
			cfg := &config.Config{}
			cfg.Gateway.ModelsListReadMaxBytes = 256
			svc := NewCodexEngineCapacitySyncService(repo, upstream, cfg)
			svc.timeout = 20 * time.Millisecond
			if name == "proxy" {
				id := int64(9)
				account.ProxyID = &id
			}
			err := svc.syncAccountModels(ctx, &account)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private credentials")
			require.Equal(t, [][]string{{"last-success"}}, repo.models)
			if name == "proxy" {
				require.Zero(t, calls)
			}
		})
	}
}

func TestCodexEngineObservationsAreParallelAndIndependent(t *testing.T) {
	for _, fail := range []string{"", "capacity", "models"} {
		t.Run(fail, func(t *testing.T) {
			account := capacitySyncTestAccount(1)
			repo := &capacitySyncTestRepo{}
			started := make(chan string, 2)
			release := make(chan struct{})
			upstream := &capacitySyncTestUpstream{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				kind := "capacity"
				if strings.HasSuffix(req.URL.Path, "/models") {
					kind = "models"
				}
				started <- kind
				select {
				case <-release:
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
				if kind == fail {
					return capacitySyncTestResponse(404, "{}"), nil
				}
				if kind == "models" {
					return capacitySyncTestResponse(200, `{"object":"list","data":[{"id":"gpt-test"}]}`), nil
				}
				return capacitySyncTestResponse(200, capacitySyncTestDocument(0)), nil
			}}
			svc := NewCodexEngineCapacitySyncService(repo, upstream, nil)
			done := make(chan error, 1)
			go func() { done <- svc.syncAccountObservations(t.Context(), &account) }()
			for range 2 {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("both observations must start before either finishes")
				}
			}
			close(release)
			err := <-done
			if fail == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if fail != "capacity" {
				require.Equal(t, []int{0}, repo.limits)
			} else {
				require.Empty(t, repo.limits)
			}
			if fail != "models" {
				require.Equal(t, [][]string{{"gpt-test"}}, repo.models)
			} else {
				require.Empty(t, repo.models)
			}
		})
	}
}

func TestCodexEngineKnownEmptyModelsDenyRouting(t *testing.T) {
	account := capacitySyncTestAccount(1)
	account.Extra[CodexEngineModelsSyncExtraKey] = map[string]any{
		"known": true, "identity": "identity", "observation_id": "observation", "model_count": 0,
	}
	require.False(t, account.IsModelSupported("gpt-test"))
	require.Equal(t, 0, CodexEngineModelsStateFromAccount(&account).ModelCount)
	raw, err := json.Marshal(account.Extra)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &account.Extra))
	require.False(t, account.IsModelSupported("gpt-test"), "JSON cache round trip preserves explicit empty")
	state := account.Extra[CodexEngineModelsSyncExtraKey].(map[string]any)
	state["model_count"] = 1
	account.Credentials["model_mapping"] = map[string]any{"gpt-test": "gpt-test"}
	require.True(t, account.IsModelSupported("gpt-test"))
	require.False(t, account.IsModelSupported("gpt-other"))
	// A normal API Key keeps its existing empty-mapping behavior.
	account.Extra[OpenAIAPIKeyModeExtraKey] = "generic"
	delete(account.Credentials, "model_mapping")
	require.Nil(t, CodexEngineModelsStateFromAccount(&account))
	require.True(t, account.IsModelSupported("gpt-test"))
}

func TestCodexEngineModelsInvalidStateIsNotKnownEmpty(t *testing.T) {
	for _, count := range []any{nil, -1, 0.5, "0"} {
		account := capacitySyncTestAccount(1)
		account.Extra[CodexEngineModelsSyncExtraKey] = map[string]any{
			"known": true, "identity": "identity", "observation_id": "observation", "model_count": count,
		}
		require.Nil(t, CodexEngineModelsStateFromAccount(&account))
	}
}
