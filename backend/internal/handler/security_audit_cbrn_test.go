package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func combinationClientErrorDecision(code string) *securityaudit.Decision {
	decision := keywordClientErrorDecision()
	decision.ErrorCode = code
	decision.Prompt.ErrorCode = code
	decision.ClientMessage = `请求同时命中危险主题与操作意图（主题命中："主题\n"；操作意图命中："步骤"）`
	return decision
}

func TestPromptCombinationClientMessageHTTPEnvelopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, code := range []string{securityaudit.ErrorCodeCBRNBlocked, securityaudit.ErrorCodeCombinationBlocked} {
		for _, tc := range []struct {
			name   string
			write  func(*gin.Context, *securityaudit.Decision)
			google bool
		}{
			{name: "openai", write: (&OpenAIGatewayHandler{}).openAISecurityAuditError},
			{name: "chat", write: (&GatewayHandler{}).openAISecurityAuditError},
			{name: "responses", write: (&GatewayHandler{}).responsesSecurityAuditError},
			{name: "messages", write: (&GatewayHandler{}).anthropicSecurityAuditError},
			{name: "gemini", write: googleSecurityAuditError, google: true},
		} {
			t.Run(code+"/"+tc.name, func(t *testing.T) {
				decision := combinationClientErrorDecision(code)
				c, recorder := securityAuditErrorTestContext(t)
				tc.write(c, decision)
				require.Equal(t, http.StatusForbidden, recorder.Code)
				errorObject := requireKeywordClientMessage(t, recorder.Body.Bytes(), decision.ClientMessage)
				if tc.google {
					details := requireArray(t, errorObject["details"])
					require.Equal(t, code, requireObject(t, details[0])["reason"])
				} else {
					require.Equal(t, code, errorObject["code"])
				}
			})
		}
	}
}

func TestPromptCombinationClientMessageWebSocket(t *testing.T) {
	for _, code := range []string{securityaudit.ErrorCodeCBRNBlocked, securityaudit.ErrorCodeCombinationBlocked} {
		t.Run(code, func(t *testing.T) {
			decision := combinationClientErrorDecision(code)
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
			_, raw, err := conn.Read(ctx)
			require.NoError(t, err)
			errorObject := requireKeywordClientMessage(t, raw, decision.ClientMessage)
			require.Equal(t, code, errorObject["code"])
			_, _, err = conn.Read(ctx)
			require.Equal(t, coderws.StatusCode(4403), coderws.CloseStatus(err))
		})
	}
}
