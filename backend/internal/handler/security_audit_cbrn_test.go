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

func cbrnClientErrorDecision() *securityaudit.Decision {
	decision := keywordClientErrorDecision()
	decision.ErrorCode = securityaudit.ErrorCodeCBRNBlocked
	decision.Prompt.ErrorCode = securityaudit.ErrorCodeCBRNBlocked
	decision.ClientMessage = `请求同时命中危险主题与操作意图（主题命中："主题\n"；操作意图命中："步骤"）`
	return decision
}

func TestPromptCBRNClientMessageHTTPEnvelopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
		t.Run(tc.name, func(t *testing.T) {
			decision := cbrnClientErrorDecision()
			c, recorder := securityAuditErrorTestContext(t)
			tc.write(c, decision)
			require.Equal(t, http.StatusForbidden, recorder.Code)
			errorObject := requireKeywordClientMessage(t, recorder.Body.Bytes(), decision.ClientMessage)
			if tc.google {
				details := requireArray(t, errorObject["details"])
				require.Equal(t, securityaudit.ErrorCodeCBRNBlocked, requireObject(t, details[0])["reason"])
			} else {
				require.Equal(t, securityaudit.ErrorCodeCBRNBlocked, errorObject["code"])
			}
		})
	}
}

func TestPromptCBRNClientMessageWebSocket(t *testing.T) {
	decision := cbrnClientErrorDecision()
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
	require.Equal(t, securityaudit.ErrorCodeCBRNBlocked, errorObject["code"])
	_, _, err = conn.Read(ctx)
	require.Equal(t, coderws.StatusCode(4403), coderws.CloseStatus(err))
}
