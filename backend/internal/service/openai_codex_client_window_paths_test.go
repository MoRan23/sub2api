package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func codexClientWindowPathUUID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	return id.String()
}

func codexClientWindowPathBody(t *testing.T, ws bool, session string, number uint64, first, current, previous string, includeBlock bool) []byte {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"session_id": session, "thread_id": session,
		"turn_id": codexClientWindowPathUUID(t), "request_kind": "turn",
		"window_id":     fmt.Sprintf("%s:%d", session, number),
		"window_number": number, "context_window_id": current,
	})
	require.NoError(t, err)
	input := []any{}
	if includeBlock {
		input = append(input, map[string]any{
			"type": "message", "role": "developer",
			"content": []any{map[string]any{
				"type": "input_text", "text": contextWindowBodyPathBlock(first, current, previous),
			}},
		})
	}
	input = append(input, map[string]any{"role": "user", "content": "Continue the task."})
	body := map[string]any{
		"model": "gpt-5.4", "stream": true, "input": input,
		"client_metadata": map[string]any{openAIWSTurnMetadataHeader: string(metadata)},
	}
	if ws {
		body["type"] = "response.create"
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return encoded
}

// Build at the same transport seams as production. In particular, the bridge
// actually forwards its HTTP request through the in-process upstream recorder.
func codexClientWindowPathBuild(t *testing.T, transport string, svc *OpenAIGatewayService, upstream *httpUpstreamRecorder, account *Account, c *gin.Context, body []byte) ([]byte, OpenAIOAuthIdentityPlan) {
	t.Helper()
	// HTTP and WS ingress capture once before routing; retain the same immutable
	// capture when rebuilding an already-started physical attempt.
	if _, captured := OpenAIOAuthIdentityCaptureFromContext(c); !captured {
		SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))
	}
	var outbound []byte
	switch transport {
	case "http", "oauth_passthrough":
		var req *http.Request
		var err error
		if transport == "http" {
			req, err = svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "oauth-token", true, "", false)
		} else {
			req, err = svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
		}
		require.NoError(t, err)
		require.NotContains(t, req.URL.Path, "/compact")
		outbound = readOpenAIIdentityPathRequestBody(t, req)
	case "websocket":
		c.Request.Method = http.MethodGet
		_, resolution, err := svc.buildOpenAIWSHeadersWithBody(c.Request.Context(), c, account, "oauth-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, true, "", "", "", body, true)
		require.NoError(t, err)
		plan, err := svc.finalizeOpenAIOAuthWSWirePlan(c, account, resolution.OutboundIdentityPlan, body, openAIOAuthWSWireFinalizeOptions{FinalModel: "gpt-5.4"})
		require.NoError(t, err)
		outbound, err = svc.projectOpenAIOAuthWSFrame(c, account, plan, body)
		require.NoError(t, err)
	case "websocket_http_bridge":
		c.Request.Method = http.MethodGet
		upstream.resp = openAICompatSSECompletedResponse("resp_client_window_bridge", "gpt-5.4")
		frames := 0
		result, err := svc.proxyOpenAIWSHTTPBridgeTurn(c.Request.Context(), c, account, "oauth-token", body, len(body), "gpt-5.4", "", "", "", "", 1, func([]byte) error { frames++; return nil })
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Positive(t, frames)
		require.NotNil(t, upstream.lastReq)
		require.NotContains(t, upstream.lastReq.URL.Path, "/compact")
		outbound = bytes.Clone(upstream.lastBody)
		require.False(t, gjson.GetBytes(outbound, "type").Exists())
	default:
		t.Fatalf("unsupported test transport %q", transport)
	}
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.True(t, plan.WireProfile.Finalized)
	require.Equal(t, CodexWireRequestTurn, plan.WireProfile.RequestKind)
	require.False(t, HasCompactionTriggerInInput(outbound))
	return outbound, plan
}

func requireCodexClientWindowPathProjection(t *testing.T, body []byte, plan OpenAIOAuthIdentityPlan, clientIDs ...string) {
	t.Helper()
	require.NoError(t, ValidateOpenAICodexWindowSnapshot(plan.Window))
	nested := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	windowNumber := gjson.Get(nested, "window_number")
	require.Equal(t, gjson.Number, windowNumber.Type)
	require.Equal(t, plan.Window.Number, windowNumber.Uint())
	require.Equal(t, plan.Window.WindowID(), gjson.Get(nested, "window_id").String())
	require.Equal(t, plan.Window.ContextWindowID, gjson.Get(nested, "context_window_id").String())
	require.Equal(t, contextWindowBodyPathBlock(plan.Window.FirstContextWindowID, plan.Window.ContextWindowID, plan.Window.PreviousContextWindowID), gjson.GetBytes(body, "input.0.content.0.text").String())
	for _, clientID := range clientIDs {
		if clientID != "" {
			require.NotContains(t, string(body), clientID, "client window IDs must not escape through body or metadata")
		}
	}
}

func TestOpenAICodexClientWindowPathsTokenBudgetRollover(t *testing.T) {
	for _, transport := range []string{"http", "oauth_passthrough", "websocket", "websocket_http_bridge"} {
		t.Run(transport, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			// Existing identity convergence also handles client rollovers when the
			// newly added PAT History/Notes feature remains disabled.
			require.False(t, svc.settingService.IsOpenAICodexPATContextManagementEnabled(context.Background()))
			account := newOpenAIIdentityPathOAuthAccount(927300)
			session := codexClientWindowPathUUID(t)
			clientIDs := []string{codexClientWindowPathUUID(t), codexClientWindowPathUUID(t), codexClientWindowPathUUID(t)}
			isWS := transport == "websocket" || transport == "websocket_http_bridge"
			var plans []OpenAIOAuthIdentityPlan
			var contexts []*gin.Context
			var bodies [][]byte
			for number := uint64(0); number < 3; number++ {
				previous := ""
				if number > 0 {
					previous = clientIDs[number-1]
				}
				body := codexClientWindowPathBody(t, isWS, session, number, clientIDs[0], clientIDs[number], previous, true)
				original := bytes.Clone(body)
				c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 927301)
				outbound, plan := codexClientWindowPathBuild(t, transport, svc, upstream, account, c, body)
				require.Equal(t, number, plan.Window.Number, "TokenBudget rollover must advance without any compact request")
				requireCodexClientWindowPathProjection(t, outbound, plan, clientIDs...)
				require.Equal(t, original, body)
				if number == 0 {
					require.Equal(t, plan.Window.ContextWindowID, plan.Window.FirstContextWindowID)
					require.Empty(t, plan.Window.PreviousContextWindowID)
				} else {
					require.Equal(t, plans[0].Window.FirstContextWindowID, plan.Window.FirstContextWindowID)
					require.Equal(t, plans[number-1].Window.ContextWindowID, plan.Window.PreviousContextWindowID)
					require.NotEqual(t, plans[number-1].Window.ContextWindowID, plan.Window.ContextWindowID)
					require.Equal(t, plans[0].Window.ThreadID, plan.Window.ThreadID)
					require.Equal(t, plans[0].WindowMappingKey, plan.WindowMappingKey)
				}
				plans = append(plans, plan)
				contexts = append(contexts, c)
				bodies = append(bodies, body)

				// A new inbound request with the same client window is idempotent.
				retry, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 927301)
				retriedBody, retryPlan := codexClientWindowPathBuild(t, transport, svc, upstream, account, retry, body)
				require.Equal(t, plan.Window, retryPlan.Window)
				requireCodexClientWindowPathProjection(t, retriedBody, retryPlan, clientIDs...)
			}

			// Physical retries retain their original immutable snapshot even after
			// two later client windows have become current in the shared store.
			for number, c := range contexts {
				outbound, retryPlan := codexClientWindowPathBuild(t, transport, svc, upstream, account, c, bodies[number])
				require.Equal(t, plans[number].Window, retryPlan.Window)
				requireCodexClientWindowPathProjection(t, outbound, retryPlan, clientIDs...)
			}
		})
	}
}

func TestOpenAICodexClientWindowPathsUnmarkedTurnCannotAdvance(t *testing.T) {
	for _, transport := range []string{"http", "oauth_passthrough", "websocket", "websocket_http_bridge"} {
		t.Run(transport, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc, _ := newOpenAIIdentityPathService(t, true, upstream)
			account := newOpenAIIdentityPathOAuthAccount(927302)
			session := codexClientWindowPathUUID(t)
			first, next := codexClientWindowPathUUID(t), codexClientWindowPathUUID(t)
			isWS := transport == "websocket" || transport == "websocket_http_bridge"
			var initial OpenAICodexWindowSnapshot
			for number, current := range []string{first, next} {
				body := codexClientWindowPathBody(t, isWS, session, uint64(number), first, current, first, false)
				c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 927303)
				outbound, plan := codexClientWindowPathBuild(t, transport, svc, upstream, account, c, body)
				require.Zero(t, plan.Window.Number, "ordinary turn metadata alone cannot request a rollover")
				require.False(t, gjson.GetBytes(outbound, `input.#(role=="developer")`).Exists(), "ordinary turns do not acquire a context-window carrier")
				if number == 0 {
					initial = plan.Window
				} else {
					require.Equal(t, initial, plan.Window)
				}
			}
		})
	}
}
