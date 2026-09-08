package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const contextWindowBodyPathClientID = "01989f44-7c00-7000-8000-000000000901"

func contextWindowBodyPathBlock(first, current, previous string) string {
	text := "<context_window>\nAgent name: Codex\nFirst context window id: " + first + "\nCurrent context window id: " + current
	if previous != "" {
		text += "\nPrevious context window id: " + previous
	}
	return text + "\n</context_window>"
}

func contextWindowBodyPathPayload(t *testing.T, ws bool, first, current, previous string) []byte {
	t.Helper()
	block := contextWindowBodyPathBlock(first, current, previous)
	nested := fmt.Sprintf(`{"session_id":%q,"thread_id":%q,"turn_id":%q,"request_kind":"turn"}`, codexWireTestSession, codexWireTestSession, codexWireTestTurn)
	body := map[string]any{
		"model": "gpt-5.4", "stream": true,
		"input": []any{
			map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": block}}},
			map[string]any{"role": "user", "content": block},
		},
		"client_metadata": map[string]any{openAIWSTurnMetadataHeader: nested},
	}
	if ws {
		body["type"] = "response.create"
	}
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return encoded
}

func requireContextWindowBodyPathProjection(t *testing.T, body []byte, plan OpenAIOAuthIdentityPlan, untouchedUser string) {
	t.Helper()
	nested := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, plan.Window.ContextWindowID, gjson.Get(nested, "context_window_id").String())
	require.Equal(t, plan.Window.Number, gjson.Get(nested, "window_number").Uint())
	require.Equal(t, contextWindowBodyPathBlock(plan.Window.FirstContextWindowID, plan.Window.ContextWindowID, plan.Window.PreviousContextWindowID), gjson.GetBytes(body, "input.0.content.0.text").String())
	require.Equal(t, untouchedUser, gjson.GetBytes(body, "input.1.content").String(), "user-authored text is not an identity carrier")
}

// Exercise the builders/finalizer that freeze an actual outbound window, then
// replay that immutable plan after the store advances. A new plan must use the
// advanced history while an already-started physical retry retains the old one.
func TestOpenAICodexContextWindowBodyPathsFrozenRetryAndRotation(t *testing.T) {
	for _, transport := range []string{"http", "oauth_passthrough", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			isWS := transport == "websocket"
			body := contextWindowBodyPathPayload(t, isWS, contextWindowBodyPathClientID, contextWindowBodyPathClientID, "")
			original := bytes.Clone(body)
			userText := gjson.GetBytes(body, "input.1.content").String()
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 921)
			svc, _ := newOpenAIIdentityPathService(t, true, nil)
			account := newOpenAIIdentityPathOAuthAccount(919021)
			var outbound []byte
			var plan OpenAIOAuthIdentityPlan
			var err error
			switch transport {
			case "http", "oauth_passthrough":
				var req *http.Request
				if transport == "http" {
					req, err = svc.buildUpstreamRequest(c.Request.Context(), c, account, body, "oauth-token", true, "", false)
				} else {
					req, err = svc.buildUpstreamRequestOpenAIPassthrough(c.Request.Context(), c, account, body, "oauth-token")
				}
				require.NoError(t, err)
				outbound = readOpenAIIdentityPathRequestBody(t, req)
				var ok bool
				plan, ok = OpenAIOAuthIdentityPlanFromContext(c)
				require.True(t, ok)
			case "websocket":
				c.Request.Method = http.MethodGet
				_, resolution, buildErr := svc.buildOpenAIWSHeadersWithBody(context.Background(), c, account, "oauth-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, true, "", "", "", body, true)
				require.NoError(t, buildErr)
				plan, err = svc.finalizeOpenAIOAuthWSWirePlan(c, account, resolution.OutboundIdentityPlan, body, openAIOAuthWSWireFinalizeOptions{FinalModel: "gpt-5.4"})
				require.NoError(t, err)
				outbound, err = svc.projectOpenAIOAuthWSFrame(c, account, plan, body)
				require.NoError(t, err)
			}
			require.True(t, plan.WireProfile.Finalized)
			require.Zero(t, plan.Window.Number)
			require.NotEqual(t, contextWindowBodyPathClientID, plan.Window.ContextWindowID)
			requireContextWindowBodyPathProjection(t, outbound, plan, userText)
			require.Equal(t, original, body, "outbound projection must not mutate inbound capture")

			store := newOpenAICodexWindowLocalStore(4)
			stored, err := store.ResolveOpenAICodexWindow(context.Background(), plan.WindowMappingKey, plan.Window, time.Hour)
			require.NoError(t, err)
			nextID := "01989f44-7c00-7000-8000-000000000902"
			committed, err := store.CommitOpenAICodexWindow(context.Background(), plan.WindowMappingKey, stored, strings.Repeat("a", 64), nextID, time.Hour)
			require.NoError(t, err)
			require.Equal(t, OpenAICodexWindowCommitAdvanced, committed.Status)
			nextPlan, err := BindOpenAICodexWindowToPlan(plan, committed.Snapshot, plan.WindowMappingKey)
			require.NoError(t, err)
			require.Equal(t, plan.Window.ContextWindowID, nextPlan.Window.PreviousContextWindowID)
			require.Equal(t, plan.Window.FirstContextWindowID, nextPlan.Window.FirstContextWindowID)

			project := func(p OpenAIOAuthIdentityPlan, input []byte) []byte {
				t.Helper()
				var out []byte
				if isWS {
					out, err = svc.projectOpenAIOAuthWSFrame(c, account, p, input)
				} else {
					out, err = ApplyOpenAIOAuthIdentityPlan(make(http.Header), input, p)
				}
				require.NoError(t, err)
				return out
			}
			requireContextWindowBodyPathProjection(t, project(plan, body), plan, userText)
			nextBody := contextWindowBodyPathPayload(t, isWS, contextWindowBodyPathClientID, "01989f44-7c00-7000-8000-000000000903", contextWindowBodyPathClientID)
			nextOriginal := bytes.Clone(nextBody)
			requireContextWindowBodyPathProjection(t, project(nextPlan, nextBody), nextPlan, gjson.GetBytes(nextBody, "input.1.content").String())
			require.Equal(t, original, body)
			require.Equal(t, nextOriginal, nextBody)

			// The second rollover has three distinct server IDs. None of the
			// client's first/previous/current IDs match them, so passing cannot be
			// explained by the old first == current substitution shortcut.
			second, err := store.CommitOpenAICodexWindow(context.Background(), plan.WindowMappingKey, committed.Snapshot, strings.Repeat("b", 64), "01989f44-7c00-7000-8000-000000000904", time.Hour)
			require.NoError(t, err)
			secondPlan, err := BindOpenAICodexWindowToPlan(plan, second.Snapshot, plan.WindowMappingKey)
			require.NoError(t, err)
			require.Equal(t, uint64(2), secondPlan.Window.Number)
			require.NotEqual(t, secondPlan.Window.FirstContextWindowID, secondPlan.Window.PreviousContextWindowID)
			require.NotEqual(t, secondPlan.Window.PreviousContextWindowID, secondPlan.Window.ContextWindowID)
			secondBody := contextWindowBodyPathPayload(t, isWS, contextWindowBodyPathClientID, "01989f44-7c00-7000-8000-000000000905", "01989f44-7c00-7000-8000-000000000903")
			secondOriginal := bytes.Clone(secondBody)
			requireContextWindowBodyPathProjection(t, project(secondPlan, secondBody), secondPlan, gjson.GetBytes(secondBody, "input.1.content").String())
			require.Equal(t, secondOriginal, secondBody)
			missingPrevious := contextWindowBodyPathPayload(t, isWS, contextWindowBodyPathClientID, "01989f44-7c00-7000-8000-000000000905", "")
			requireContextWindowBodyPathProjection(t, project(secondPlan, missingPrevious), secondPlan, gjson.GetBytes(missingPrevious, "input.1.content").String())
			requireContextWindowBodyPathProjection(t, project(plan, body), plan, userText)
		})
	}
}

func TestOpenAICodexContextWindowBodyPathsWSHTTPBridge(t *testing.T) {
	body := contextWindowBodyPathPayload(t, true, contextWindowBodyPathClientID, contextWindowBodyPathClientID, "")
	original := bytes.Clone(body)
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_context_window_bridge", "gpt-5.4")}
	svc, _ := newOpenAIIdentityPathService(t, true, upstream)
	account := newOpenAIIdentityPathOAuthAccount(919022)
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 922)
	c.Request.Method = http.MethodGet
	frames := 0
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "oauth-token", body, len(body), "gpt-5.4", "", "", "", "", 1, func([]byte) error { frames++; return nil })
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Positive(t, frames)
	require.NotNil(t, upstream.lastReq)
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	requireContextWindowBodyPathProjection(t, upstream.lastBody, plan, gjson.GetBytes(body, "input.1.content").String())
	require.Equal(t, original, body)
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists(), "bridge emits the physical HTTP Responses shape")
}
