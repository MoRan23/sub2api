package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type excelMemoryBackend struct {
	mu     sync.Mutex
	values map[string]string
	broken bool
}

func (b *excelMemoryBackend) PutOpenAIExcelState(_ context.Context, scope, key, value string, _ time.Duration, _ int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.broken {
		return errors.New("storage unavailable")
	}
	b.values[scope+key] = value
	return nil
}
func (b *excelMemoryBackend) GetOpenAIExcelState(_ context.Context, scope, key string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.broken {
		return "", errors.New("storage unavailable")
	}
	value, ok := b.values[scope+key]
	if !ok {
		return "", ErrOpenAIExcelStateNotFound
	}
	return value, nil
}

type excelTestCipher struct{}

func (excelTestCipher) Encrypt(value string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(value)), nil
}
func (excelTestCipher) Decrypt(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	return string(decoded), err
}
func excelStateFixture() (*OpenAIExcelStateStore, *excelMemoryBackend) {
	b := &excelMemoryBackend{values: map[string]string{}}
	return NewOpenAIExcelStateStore(b, excelTestCipher{}), b
}

func TestOpenAIExcelNativeHistoryTenantIsolationAndRestart(t *testing.T) {
	store, backend := excelStateFixture()
	ctx := context.Background()
	item := json.RawMessage(`{"type":"function_call","id":"fc_native","call_id":"call_1","name":"run_officejs","arguments":"{\"code\":\"sensitive command\"}","token":"must-not-persist","output":"must-not-persist"}`)
	require.NoError(t, store.StoreExcelNativeCall(ctx, "account1:grant1:route1:tenant1:session1", "call_1", item))
	restarted := NewOpenAIExcelStateStore(backend, excelTestCipher{})
	value, err := restarted.LoadExcelNativeCall(ctx, "account1:grant1:route1:tenant1:session1", "call_1")
	require.NoError(t, err)
	require.Contains(t, string(value), "run_officejs")
	require.NotContains(t, string(value), "must-not-persist")
	for _, scope := range []string{"account2:grant1:route1:tenant1:session1", "account1:grant2:route1:tenant1:session1", "account1:grant1:route2:tenant1:session1", "account1:grant1:route1:tenant2:session1"} {
		_, err = restarted.LoadExcelNativeCall(ctx, scope, "call_1")
		require.ErrorIs(t, err, ErrOpenAIExcelHistoryNotFound)
	}
	for key, value := range backend.values {
		require.NotContains(t, key, "tenant1")
		require.NotContains(t, value, "sensitive command")
	}
}

func TestOpenAIExcelStateRejectsSwappedCiphertextAndStorageFailure(t *testing.T) {
	store, backend := excelStateFixture()
	ctx := context.Background()
	require.NoError(t, store.put(ctx, "session1", "native", "call", json.RawMessage(`{"id":"safe"}`), time.Hour, 512))
	for _, value := range backend.values {
		backend.values[excelStateDigest("session2|native")+excelStateDigest("call")] = value
		break
	}
	_, err := store.get(ctx, "session2", "native", "call")
	require.ErrorIs(t, err, ErrOpenAIExcelStateUnavailable)
	backend.broken = true
	require.ErrorIs(t, store.StoreExcelNativeCall(ctx, "session", "call", json.RawMessage(`{"id":"x"}`)), ErrOpenAIExcelStateUnavailable)
}

func TestOpenAIExcelBackendProvenanceBlocksCrossRouteAndOpaqueInput(t *testing.T) {
	store, _ := excelStateFixture()
	svc := &OpenAIGatewayService{excelState: store}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true, OpenAIUpstreamRouteGenerationExtraKey: "route1"}}
	ctx := context.Background()
	response := []byte(`{"type":"response.completed","response":{"id":"resp_one","output":[{"type":"reasoning","encrypted_content":"opaque-secret"}]}}`)
	require.NoError(t, svc.rememberOpenAIBackendPayload(ctx, account, "excel1", response, nil))
	input := []byte(`{"previous_response_id":"resp_one","input":[{"type":"reasoning","encrypted_content":"opaque-secret"}]}`)
	require.NoError(t, svc.validateOpenAIBackendPayload(ctx, account, "excel1", input, nil))
	require.ErrorIs(t, svc.validateOpenAIBackendPayload(ctx, account, "codex2", input, nil), ErrOpenAIBackendHistoryUnavailable)
	require.ErrorIs(t, svc.validateOpenAIBackendPayload(ctx, account, "excel1", []byte(`{"input":[{"type":"reasoning","encrypted_content":"unknown"}]}`), nil), ErrOpenAIBackendHistoryUnavailable)
}

func TestOpenAIExcelProvenanceStreamPreservesFailureAndEOF(t *testing.T) {
	store, _ := excelStateFixture()
	svc := &OpenAIGatewayService{excelState: store}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIExcelUpstreamEnabledExtraKey: true}}
	request, err := http.NewRequest(http.MethodPost, openAIExcelAPIBase+"/responses", strings.NewReader(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	source := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_bad\"}}\n\n"
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(source))}
	response = svc.wrapOpenAIBackendResponse(request, response, account, "scope")
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, source, string(body))
	require.ErrorIs(t, svc.validateOpenAIBackendPayload(context.Background(), account, "scope", []byte(`{"previous_response_id":"resp_bad"}`), nil), ErrOpenAIBackendHistoryUnavailable)
}

func TestCandyTestRouteSnapshotAndLegacyQueuedJob(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	require.True(t, candyTestRouteMatches(&CandyTestItem{}, account))
	account.Extra[OpenAIExcelUpstreamEnabledExtraKey] = true
	account.Extra[OpenAIUpstreamRouteGenerationExtraKey] = "r1"
	require.False(t, candyTestRouteMatches(&CandyTestItem{}, account))
	item := &CandyTestItem{ExpectedUpstreamKind: "excel", ExpectedRouteGeneration: "r1"}
	require.True(t, candyTestRouteMatches(item, account))
	account.Extra[OpenAIUpstreamRouteGenerationExtraKey] = "r2"
	require.False(t, candyTestRouteMatches(item, account))
}

func TestOpenAIExcelHistoryScopeRequiresGrantAndSeparatesReauthorization(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := excelTransportAccount()
	headers := http.Header{"Session_id": {"logical-session"}}
	ctx := context.WithValue(context.Background(), openAIExcelRequestScopeKey{}, openAIExcelRequestScope{Tenant: "key:1", Session: "logical-session"})
	first := svc.openAIExcelHistoryScope(ctx, account, headers, nil)
	require.NotEmpty(t, first)
	account.OpenAIOAuthAuthorizationGeneration = "replacement-grant"
	require.NotEqual(t, first, svc.openAIExcelHistoryScope(ctx, account, headers, nil))
	account.OpenAIOAuthAuthorizationGeneration = ""
	require.Empty(t, svc.openAIExcelHistoryScope(ctx, account, headers, nil), "account ID must not substitute for authorization generation")
}

func TestOpenAIExcelProvenanceMultilineSSE(t *testing.T) {
	store, _ := excelStateFixture()
	svc := &OpenAIGatewayService{excelState: store}
	account := excelTransportAccount()
	req, err := http.NewRequest("POST", openAIExcelResponsesURL, strings.NewReader(`{}`))
	require.NoError(t, err)
	stream := "event: response.completed\ndata: {\ndata: \"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp_multiline\",\"status\":\"completed\"}}\n\n"
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	resp = svc.wrapOpenAIBackendResponse(req, resp, account, "scope")
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, stream, string(got))
	_, err = store.get(context.Background(), "scope", "provenance", "response:resp_multiline")
	require.NoError(t, err, "multiline events must register the same response as single-line events")
}
