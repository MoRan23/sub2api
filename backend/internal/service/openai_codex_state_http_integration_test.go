package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the real Cookie send/capture boundary without opening a socket.
type codexStateCookieIntegrationUpstream struct {
	*httpUpstreamRecorder
	manager *openaicookies.Manager
	account *Account
}

func (u *codexStateCookieIntegrationUpstream) Do(request *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	ctx := openaicookies.WithScope(request.Context(), openaicookies.Scope{OwnerAccountID: u.account.ID, OSFamily: u.account.OpenAIOAuthCredentialOS, AuthorizationGeneration: u.account.OpenAIOAuthAuthorizationGeneration})
	return u.manager.Wrap(codexCookieAtomicRoundTripper(func(wire *http.Request) (*http.Response, error) {
		return u.httpUpstreamRecorder.Do(wire, proxy, id, concurrency)
	})).RoundTrip(request.WithContext(ctx))
}

func (u *codexStateCookieIntegrationUpstream) DoWithTLS(request *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(request, proxy, id, concurrency)
}

func codexStateHTTPIntegrationResponse(token, carrier string, failed bool) *http.Response {
	response := openAICompatSSECompletedResponse("resp_turn_state_test", "gpt-5.4")
	completed, _ := io.ReadAll(response.Body)
	var stream strings.Builder
	if carrier == "header" {
		response.Header.Set("x-codex-turn-state", token)
	} else {
		event, _ := json.Marshal(map[string]any{"type": "response.metadata", "headers": map[string]string{"x-codex-turn-state": token}})
		fmt.Fprintf(&stream, "data: %s\n\n", event)
	}
	if failed {
		stream.WriteString("data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"status\":\"failed\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"test response rejected\"}}}\n\n")
	} else {
		stream.WriteString("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_turn_state_test\",\"model\":\"gpt-5.4\",\"status\":\"in_progress\"}}\n\n")
		stream.WriteString("data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n")
		stream.Write(completed)
		stream.WriteByte('\n') // Terminate the final SSE frame for strict collectors.
	}
	response.Body = io.NopCloser(strings.NewReader(stream.String()))
	return response
}

func codexStateHTTPIntegrationBody(path string, stream bool) []byte {
	if path == "chat" || path == "messages" {
		return []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":` + strconv.FormatBool(stream) + `}`)
	}
	return []byte(`{"model":"gpt-5.4","instructions":"Reply briefly.","input":[{"role":"user","content":"hello"}],"stream":` + strconv.FormatBool(stream) + `}`)
}

func codexStateHTTPIntegrationForward(t *testing.T, svc *OpenAIGatewayService, account *Account, path string, body []byte) (*OpenAIForwardResult, *httptest.ResponseRecorder, error) {
	t.Helper()
	if svc.accountRepo == nil && svc.codexTurnStateService != nil {
		svc.accountRepo = svc.codexTurnStateService.accounts
	}
	url := "/v1/responses"
	if path == "chat" {
		url = "/v1/chat/completions"
	}
	if path == "messages" {
		url = "/v1/messages"
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	var result *OpenAIForwardResult
	var err error
	switch path {
	case "chat":
		result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	case "messages":
		result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	default:
		result, err = svc.Forward(context.Background(), c, account, body)
	}
	return result, recorder, err
}

func TestCodexTurnStateHTTPGatewayNaturalResponseAllPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, carrier := range []string{"header", "metadata"} {
				t.Run(path+"/stream="+strconv.FormatBool(stream)+"/"+carrier, func(t *testing.T) {
					state, repo, account := newCodexStateTestService(t)
					state.now = time.Now
					account.Concurrency = 1
					if path == "passthrough" {
						account.Extra["openai_passthrough"] = true
					}
					token := codexStateTestToken(10, state.now())
					upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(token, carrier, false)}, manager: openaicookies.NewManager(), account: account}
					gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
					var collected atomic.Int64
					state.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
						collected.Add(1)
						return CodexTurnStateCollectResult{}, nil
					})
					result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 1)
					finalModel := gjson.GetBytes(upstream.lastBody, "model").String()
					require.Equal(t, "gpt-5.4", finalModel)
					key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: finalModel, Generation: CodexTurnStateGenerationForAccount(account)}
					record, err := repo.Get(context.Background(), key)
					require.NoError(t, err)
					require.NotNil(t, record, "forward must bind final model")
					plain, err := state.encryptor.Decrypt(record.EncryptedToken)
					require.NoError(t, err, "delivered normal response must be learned")
					require.Equal(t, token, plain)
					require.NotEmpty(t, record.EncryptedCookieBundle, "ticket and frozen Cookie snapshot must be published together")
					state.collect(context.Background(), key)
					require.Zero(t, collected.Load(), "natural target state avoids separate collection")
				})
			}
		}
	}
}

func TestCodexTurnStateHTTPGatewayAbandonedResponsesDoNotLearn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(path+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				state, repo, account := newCodexStateTestService(t)
				state.now = time.Now
				account.Concurrency = 1
				upstream := &codexStateCookieIntegrationUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(codexStateTestToken(10, state.now()), "metadata", true)}, manager: openaicookies.NewManager(), account: account}
				gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
				_, _, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
				require.Error(t, err)
				key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: gjson.GetBytes(upstream.lastBody, "model").String(), Generation: CodexTurnStateGenerationForAccount(account)}
				record, getErr := repo.Get(context.Background(), key)
				require.NoError(t, getErr)
				require.NotNil(t, record)
				require.Empty(t, record.EncryptedToken, "metadata before a failed response must not enter reusable cache")
				require.Empty(t, record.EncryptedCookieBundle)
			})
		}
	}
}
