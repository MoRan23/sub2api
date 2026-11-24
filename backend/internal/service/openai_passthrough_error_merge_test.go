//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIPassthroughEncryptedErrorSkipsPoolRetryAndAccountPenalty(t *testing.T) {
	for _, responseBody := range []string{
		`{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified"}}`,
		`{"error":{"code":"thinking_signature_invalid","message":"The encrypted content could not be decrypted or parsed"}}`,
		`{"error":{"code":null,"message":"{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"The encrypted content could not be verified\"}}"}}`,
	} {
		t.Run(extractUpstreamErrorCode([]byte(responseBody)), func(t *testing.T) {
			repo := &errorPolicyRepoStub{}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(responseBody)),
			}}
			svc := &OpenAIGatewayService{
				cfg:              &config.Config{},
				httpUpstream:     upstream,
				rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
			}
			svc.rateLimitService.SetAccountRuntimeBlocker(svc)
			account := &Account{
				ID: 441, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
				Status: StatusActive, Schedulable: true,
				Extra: map[string]any{"openai_passthrough": true},
				Credentials: map[string]any{
					"api_key": "physical-key", "base_url": "https://upstream.example/v1",
					"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(http.StatusBadRequest)},
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{map[string]any{
						"error_code": float64(http.StatusBadRequest), "keywords": []any{"encrypted"}, "duration_minutes": float64(30),
					}},
				},
			}
			rec, c := newOpenAIPassthroughErrorTestContext()
			_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","input":"hello"}`))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.Len(t, upstream.requests, 1)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, extractUpstreamErrorCode([]byte(responseBody)), gjson.Get(rec.Body.String(), "error.code").String())
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.setErrCalls)
			require.Empty(t, repo.modelRateLimitCalls)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

			require.True(t, shouldFailoverOpenAIPassthroughResponse(account, http.StatusBadRequest,
				[]byte(`{"error":{"code":"invalid_value","message":"echo invalid_encrypted_content"}}`)), "unrelated configured pool errors retain their retry behavior")
		})
	}
}

func TestOpenAIPassthroughStructuredErrorUsesPhysicalAttemptIdentity(t *testing.T) {
	rec, c := newOpenAIPassthroughErrorTestContext()
	account := &Account{
		ID: 442, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "configured-secret", "base_url": "https://configured.example/v1", "refresh_token": "refresh-secret",
		},
	}
	request := httptest.NewRequest(http.MethodPost, "https://physical.example/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer physical-secret")
	responseBody := []byte(`{"error":{"code":"invalid_encrypted_content","type":"physical-secret","param":"PHYSICAL.EXAMPLE","message":"configured-secret refresh-secret physical-secret at CONFIGURED.EXAMPLE and physical.example"}}`)
	svc := &OpenAIGatewayService{}
	err := svc.handleErrorResponsePassthrough(context.Background(), &http.Response{StatusCode: http.StatusBadRequest}, c,
		account, []byte(`{"model":"gpt-5.4"}`), responseBody, request)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	result := rec.Body.String()
	require.Equal(t, "invalid_encrypted_content", gjson.Get(result, "error.code").String())
	require.Equal(t, "[redacted]", gjson.Get(result, "error.type").String())
	require.Equal(t, "[upstream]", gjson.Get(result, "error.param").String())
	require.Equal(t, "[redacted] [redacted] [redacted] at [upstream] and [upstream]", gjson.Get(result, "error.message").String())
	require.Equal(t, "Bearer physical-secret", request.Header.Get("Authorization"))
	require.Equal(t, "configured-secret", account.GetCredential("api_key"))

	sanitizer := newOpenAIPassthroughErrorSanitizer(account, request)
	request.Header.Set("Authorization", "Bearer later-secret")
	require.Equal(t, "[redacted] later-secret", sanitizer.sanitize("physical-secret later-secret"), "the prepared sanitizer owns only the attempt's immutable scalar facts")
}

func TestOpenAICompactStructuredFailureReportsWriterDelivery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		writer  func(gin.ResponseWriter) gin.ResponseWriter
		deliver bool
	}{
		{name: "complete", writer: func(w gin.ResponseWriter) gin.ResponseWriter { return w }, deliver: true},
		{name: "short header", writer: func(w gin.ResponseWriter) gin.ResponseWriter {
			return &shortOpenAIPassthroughRuleWriter{ResponseWriter: w, maxBytes: 8}
		}},
		{name: "short payload", writer: func(w gin.ResponseWriter) gin.ResponseWriter {
			return &shortOpenAIPassthroughRuleWriter{ResponseWriter: w, maxBytes: 32}
		}},
		{name: "failed terminator", writer: func(w gin.ResponseWriter) gin.ResponseWriter {
			return &failingGinWriter{ResponseWriter: w, failAfter: 2}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newCompactBridgeTestContext(t, true)
			c.Writer = tc.writer(c.Writer)
			require.Equal(t, tc.deliver, writeOpenAICompactSSEFailureWithCode(c, http.StatusBadRequest, "invalid_encrypted_content", "invalid reasoning"))
			require.NotContains(t, rec.Body.String(), "response.completed")
			if tc.deliver {
				events := parseCompactBridgeSSE(t, rec.Body.String())
				require.Len(t, events, 1)
				require.Equal(t, "invalid_encrypted_content", gjson.Get(events[0][1], "response.error.code").String())
				require.Positive(t, gjson.Get(events[0][1], "response.created_at").Int())
			}
		})
	}
}

func TestOpenAIPassthroughSanitizerReusesUnchangedText(t *testing.T) {
	account := &Account{Credentials: map[string]any{
		"api_key": "configured-secret", "base_url": "https://configured.example/v1", "refresh_token": "refresh-secret",
	}}
	request := httptest.NewRequest(http.MethodPost, "https://physical.example/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer physical-secret")
	sanitizer := newOpenAIPassthroughErrorSanitizer(account, request)
	for _, prefix := range []string{"upstream rejected: ", "upstream rejected?ordinary=public&other=value "} {
		input := prefix + strings.Repeat("x", 512<<10)
		got := sanitizer.sanitize(input)
		require.Equal(t, input, got)
		require.True(t, unsafe.StringData(input) == unsafe.StringData(got), "unchanged upstream text should reuse its backing bytes")
	}
	for _, input := range []string{
		"  ",
		"http://configured.example?KEY=query-secret&ordinary=public",
		"physical.example?client_secret=query-secret&ACCESS_TOKEN=token&refresh_token=refresh",
		"configured-secret refresh-secret physical-secret CONFIGURED.EXAMPLE PHYSICAL.EXAMPLE",
		"ȺK\xff configured.example PHYSICAL.EXAMPLE?key=secret\" tail",
		"configured.example?key=configured-secret&ordinary=physical-secret",
	} {
		expected := sanitizeUpstreamErrorMessage(strings.TrimSpace(input))
		for _, secret := range []string{"configured-secret", "refresh-secret", "physical-secret"} {
			expected = strings.ReplaceAll(expected, secret, "[redacted]")
		}
		for _, host := range []string{"configured.example", "physical.example"} {
			expected = replaceCaseInsensitive(expected, host, "[upstream]")
		}
		require.Equal(t, expected, sanitizer.sanitize(input))
	}
}
