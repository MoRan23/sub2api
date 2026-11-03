package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func promptGuardDecision(kind securityaudit.DecisionKind) *securityaudit.Decision {
	decision := &securityaudit.Decision{Kind: kind, AllowNextStage: false}
	switch kind {
	case securityaudit.DecisionBlock:
		decision.HTTPStatus = http.StatusForbidden
		decision.ErrorCode = securityaudit.ErrorCodeBlocked
		decision.ClientMessage = "提示词安全审计拒绝了该请求，请调整输入后重试"
	case securityaudit.DecisionInvalid:
		decision.HTTPStatus = http.StatusServiceUnavailable
		decision.ErrorCode = securityaudit.ErrorCodeInvalidResponse
		decision.ClientMessage = "提示词安全审计暂时不可用，请稍后重试"
	default:
		decision.HTTPStatus = http.StatusServiceUnavailable
		decision.ErrorCode = securityaudit.ErrorCodeUnavailable
		decision.ClientMessage = "提示词安全审计暂时不可用，请稍后重试"
	}
	return decision
}

func securityAuditErrorTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "request-error-golden")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/test", nil).WithContext(ctx)
	return c, recorder
}

func decodeErrorJSON(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload
}

func requireObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok)
	return object
}

func requireArray(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	require.True(t, ok)
	return array
}

func TestPromptGuardOpenAIAndClaudeErrorEnvelopesGolden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []securityaudit.DecisionKind{securityaudit.DecisionBlock, securityaudit.DecisionUnavailable, securityaudit.DecisionInvalid} {
		decision := promptGuardDecision(kind)
		t.Run("openai_"+string(kind), func(t *testing.T) {
			c, recorder := securityAuditErrorTestContext(t)
			(&OpenAIGatewayHandler{}).openAISecurityAuditError(c, decision)
			require.Equal(t, decision.HTTPStatus, recorder.Code)
			payload := decodeErrorJSON(t, recorder)
			errorObject := requireObject(t, payload["error"])
			require.Equal(t, decision.ErrorCode, errorObject["code"])
			if kind == securityaudit.DecisionBlock {
				require.Equal(t, "permission_error", errorObject["type"])
			} else {
				require.Equal(t, "api_error", errorObject["type"])
			}
			require.NotContains(t, recorder.Body.String(), "raw prompt")
			require.NotContains(t, recorder.Body.String(), "guard-one")
		})

		t.Run("responses_"+string(kind), func(t *testing.T) {
			c, recorder := securityAuditErrorTestContext(t)
			(&GatewayHandler{}).responsesSecurityAuditError(c, decision)
			require.Equal(t, decision.HTTPStatus, recorder.Code)
			errorObject := requireObject(t, decodeErrorJSON(t, recorder)["error"])
			require.Equal(t, decision.ErrorCode, errorObject["code"])
			require.Equal(t, "api_error", errorObject["type"])
		})

		t.Run("claude_"+string(kind), func(t *testing.T) {
			c, recorder := securityAuditErrorTestContext(t)
			(&GatewayHandler{}).anthropicSecurityAuditError(c, decision)
			require.Equal(t, decision.HTTPStatus, recorder.Code)
			payload := decodeErrorJSON(t, recorder)
			require.Equal(t, "error", payload["type"])
			errorObject := requireObject(t, payload["error"])
			require.Equal(t, decision.ErrorCode, errorObject["code"])
			if kind == securityaudit.DecisionBlock {
				require.Equal(t, "permission_error", errorObject["type"])
			} else {
				require.Equal(t, "api_error", errorObject["type"])
			}
		})
	}
}

func TestPromptGuardGeminiErrorEnvelopeGolden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []securityaudit.DecisionKind{securityaudit.DecisionBlock, securityaudit.DecisionUnavailable, securityaudit.DecisionInvalid} {
		decision := promptGuardDecision(kind)
		c, recorder := securityAuditErrorTestContext(t)
		googleSecurityAuditError(c, decision)
		require.Equal(t, decision.HTTPStatus, recorder.Code)
		payload := decodeErrorJSON(t, recorder)
		errorObject := requireObject(t, payload["error"])
		require.Equal(t, float64(decision.HTTPStatus), errorObject["code"], "Gemini code must remain numeric")
		if decision.HTTPStatus == http.StatusForbidden {
			require.Equal(t, "PERMISSION_DENIED", errorObject["status"])
		} else {
			require.Equal(t, "UNAVAILABLE", errorObject["status"])
		}
		details := requireArray(t, errorObject["details"])
		require.Len(t, details, 1)
		errorInfo := requireObject(t, details[0])
		require.Equal(t, "type.googleapis.com/google.rpc.ErrorInfo", errorInfo["@type"])
		require.Equal(t, decision.ErrorCode, errorInfo["reason"])
		require.Equal(t, "sub2api.securityaudit", errorInfo["domain"])
		metadata := requireObject(t, errorInfo["metadata"])
		require.Equal(t, map[string]any{"request_id": "request-error-golden"}, metadata)
	}
}

func TestPromptGuardWebSocketCloseMappingGolden(t *testing.T) {
	require.Equal(t, int64(4403), int64(securityAuditWSCloseStatus(promptGuardDecision(securityaudit.DecisionBlock))))
	require.Equal(t, securityaudit.ErrorCodeBlocked, securityAuditWSCloseReason(promptGuardDecision(securityaudit.DecisionBlock)))
	require.Equal(t, int64(1013), int64(securityAuditWSCloseStatus(promptGuardDecision(securityaudit.DecisionUnavailable))))
	require.Equal(t, securityaudit.ErrorCodeUnavailable, securityAuditWSCloseReason(promptGuardDecision(securityaudit.DecisionUnavailable)))
	require.Equal(t, int64(1013), int64(securityAuditWSCloseStatus(promptGuardDecision(securityaudit.DecisionInvalid))))
	require.Equal(t, securityaudit.ErrorCodeInvalidResponse, securityAuditWSCloseReason(promptGuardDecision(securityaudit.DecisionInvalid)))
}

func TestLegacyModerationErrorKeepsExistingClientPriority(t *testing.T) {
	legacy := &securityaudit.Decision{
		Kind: securityaudit.DecisionBlock, HTTPStatus: http.StatusForbidden,
		ErrorCode: "content_policy_violation", ClientMessage: "legacy exact message",
		Legacy: &securityaudit.LegacyDecision{Blocked: true, StatusCode: http.StatusForbidden, ErrorCode: "content_policy_violation", Message: "legacy exact message"},
		Prompt: &securityaudit.PromptDecision{Kind: securityaudit.DecisionBlock, ErrorCode: securityaudit.ErrorCodeBlocked},
	}
	c, recorder := securityAuditErrorTestContext(t)
	(&GatewayHandler{}).openAISecurityAuditError(c, legacy)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "legacy exact message")
	require.Contains(t, recorder.Body.String(), "content_policy_violation")
	require.NotContains(t, recorder.Body.String(), securityaudit.ErrorCodeBlocked)
}

func keywordClientErrorDecision() *securityaudit.Decision {
	decision := promptGuardDecision(securityaudit.DecisionBlock)
	decision.ErrorCode = securityaudit.ErrorCodeKeywordBlocked
	decision.ClientMessage = fmt.Sprintf("提示词包含禁止内容，请调整输入后重试（命中关键词：%q）", ".gov.\"\\中文\n\t")
	decision.Prompt = &securityaudit.PromptDecision{
		Kind: securityaudit.DecisionBlock, ErrorCode: securityaudit.ErrorCodeKeywordBlocked,
		Result: &securityaudit.NormalizedResult{
			ScannerEvidence: map[string]string{"keyword": "private-scanner-evidence"},
			GuardEndpointID: "private-guard-endpoint", PolicyID: "private-policy-config",
		},
	}
	return decision
}

func requireKeywordClientMessage(t *testing.T, raw []byte, message string) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload), "keyword punctuation must remain valid JSON")
	errorObject := requireObject(t, payload["error"])
	require.Equal(t, message, errorObject["message"])
	for _, private := range []string{"private-full-prompt", "private-scanner-evidence", "private-guard-endpoint", "private-policy-config"} {
		require.NotContains(t, string(raw), private)
	}
	return errorObject
}

func TestPromptKeywordClientMessageHTTPEnvelopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		write  func(*gin.Context, *securityaudit.Decision)
		google bool
	}{
		{name: "openai", write: (&OpenAIGatewayHandler{}).openAISecurityAuditError},
		{name: "chat", write: (&GatewayHandler{}).openAISecurityAuditError},
		{name: "responses", write: (&GatewayHandler{}).responsesSecurityAuditError},
		{name: "anthropic", write: (&GatewayHandler{}).anthropicSecurityAuditError},
		{name: "openai-anthropic", write: (&OpenAIGatewayHandler{}).anthropicSecurityAuditError},
		{name: "gemini", write: googleSecurityAuditError, google: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := keywordClientErrorDecision()
			c, recorder := securityAuditErrorTestContext(t)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"stream":true,"input":"private-full-prompt"}`)).WithContext(c.Request.Context())
			c.Request.Header.Set("Accept", "text/event-stream")
			tc.write(c, decision)
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Contains(t, recorder.Header().Get("Content-Type"), "application/json", "pre-dispatch rejection stays JSON for streaming requests")
			errorObject := requireKeywordClientMessage(t, recorder.Body.Bytes(), decision.ClientMessage)
			if tc.google {
				require.Equal(t, float64(http.StatusForbidden), errorObject["code"])
				require.Equal(t, "PERMISSION_DENIED", errorObject["status"])
				details := requireArray(t, errorObject["details"])
				require.Equal(t, securityaudit.ErrorCodeKeywordBlocked, requireObject(t, details[0])["reason"])
			} else {
				require.Equal(t, securityaudit.ErrorCodeKeywordBlocked, errorObject["code"])
			}
		})
	}
}

func TestPromptKeywordClientMessageWebSocketFrame(t *testing.T) {
	decision := keywordClientErrorDecision()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		writeSecurityAuditWSError(r.Context(), conn, decision)
		_ = conn.Close(securityAuditWSCloseStatus(decision), securityAuditWSCloseReason(decision))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	messageType, raw, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, messageType)
	errorObject := requireKeywordClientMessage(t, raw, decision.ClientMessage)
	require.Equal(t, securityaudit.ErrorCodeKeywordBlocked, errorObject["code"])
	require.Equal(t, "invalid_request_error", errorObject["type"])
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusCode(4403), coderws.CloseStatus(err))
	var closeError coderws.CloseError
	require.ErrorAs(t, err, &closeError)
	require.Equal(t, securityaudit.ErrorCodeKeywordBlocked, closeError.Reason, "keep the potentially long keyword in the JSON frame, not the close reason")
}
