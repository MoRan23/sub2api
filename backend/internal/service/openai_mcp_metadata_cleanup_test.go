package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const mcpMetadataCleanupField = "internal_chat_message_metadata_passthrough"

func mcpMetadataCleanupBody(t *testing.T, websocket bool) []byte {
	t.Helper()
	// The declared environment deliberately has no date: removing its metadata
	// before freezing eligibility would prevent structural fallback from rescuing it.
	environment := "<environment_context><cwd>/workspace</cwd><timezone>Asia/Shanghai</timezone></environment_context>"
	reference := timezoneTestEnvironment("Asia/Tokyo", "2020-01-01")
	payload := map[string]any{
		"model": "gpt-5.4", "stream": websocket, "instructions": "Keep tool results unchanged.",
		"input": []any{
			map[string]any{
				"type": "function_call", "call_id": "fc_mcp_cleanup", "name": "mcp__docs__lookup",
				"arguments":             `{"query":"keep","tool_result_metadata":{"public":true}}`,
				mcpMetadataCleanupField: map[string]any{"turn_id": "private-call-turn"},
			},
			map[string]any{
				"type": "function_call_output", "call_id": "fc_mcp_cleanup",
				"output": `{"text":"public result","tool_result_metadata":{"public":true}}`,
				mcpMetadataCleanupField: map[string]any{
					"cell_id": "private-cell", "tool_calls_complete": true,
					"executed_tool_calls": []any{map[string]any{
						"name": "mcp__docs__lookup", "arguments": map[string]any{"query": "keep"},
						"tool_result_sources":  []any{map[string]any{"type": "resource", "id": "private-resource"}},
						"tool_result_metadata": map[string]any{"private": map[string]any{"resource": "mcp-private-metadata-sentinel"}},
					}},
				},
			},
			map[string]any{
				"type": "message", "role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": environment},
					map[string]any{"type": "input_text", "text": "Explain tool_result_metadata and internal_chat_message_metadata_passthrough literally.",
						mcpMetadataCleanupField: map[string]any{"keep": true}},
					map[string]any{"type": "input_text", "text": reference},
				},
				// This last complete XML block is explicitly ordinary text. Cleanup
				// must not let a later scan grant it structural fallback eligibility.
				mcpMetadataCleanupField: map[string]any{"content_item_kinds": []any{"environments.environment_context", "text", "text"}},
			},
		},
		"tools": []any{map[string]any{"type": "function", "name": "mcp__docs__lookup", "parameters": map[string]any{"type": "object"}}},
	}
	if websocket {
		payload["type"] = "response.create"
	}
	return timezoneTestBody(t, payload)
}

func requireMCPMetadataCleanupWire(t *testing.T, original, wire []byte) {
	t.Helper()
	require.Len(t, gjson.GetBytes(wire, "input").Array(), 3)
	for i, item := range gjson.GetBytes(wire, "input").Array() {
		require.False(t, item.Get(mcpMetadataCleanupField).Exists(), "input[%d] must lose the whole metadata container", i)
	}
	require.NotContains(t, string(wire), "mcp-private-metadata-sentinel")
	for _, path := range []string{
		"input.0.type", "input.0.name", "input.0.arguments", "input.0.call_id",
		"input.1.type", "input.1.call_id", "input.1.output",
		"input.2.role", "input.2.content.1.text", "input.2.content.2.text",
		"input.2.content.1." + mcpMetadataCleanupField,
	} {
		require.Equal(t, gjson.GetBytes(original, path).Value(), gjson.GetBytes(wire, path).Value(), path)
	}
	wantEnvironment := strings.Replace(gjson.GetBytes(original, "input.2.content.0.text").String(), "Asia/Shanghai", OpenAIRequestTimezone, 1)
	require.Equal(t, wantEnvironment, gjson.GetBytes(wire, "input.2.content.0.text").String(), "freeze declared eligibility before dropping metadata")
}

func requireMCPMetadataCleanupObservation(t *testing.T, accountID int64, eventKind string) {
	t.Helper()
	var entries []FingerprintObservationEntry
	for _, entry := range SnapshotFingerprintObservations(20) {
		if entry.AccountID == accountID && (eventKind == "" || entry.EventKind == eventKind) {
			entries = append(entries, entry)
		}
	}
	require.Len(t, entries, 1)
	entry := entries[0]
	require.NotNil(t, entry.InboundTimezoneObservations)
	require.Len(t, entry.InboundTimezoneObservations.Items, 2)
	require.Equal(t, TimezoneEnvironmentSourceMetadata, entry.InboundTimezoneObservations.Items[0].EnvironmentSource)
	require.Equal(t, "Asia/Shanghai", entry.InboundTimezoneObservations.Items[0].Value)
	require.Equal(t, TimezoneEnvironmentSourceReference, entry.InboundTimezoneObservations.Items[1].EnvironmentSource)
	require.Equal(t, "matched", entry.TimezoneComparisonStatus)
	require.Len(t, entry.TimezoneConversions, 2)
	require.Equal(t, "converted", entry.TimezoneConversions[0].Status)
	require.Equal(t, "environment_metadata_missing", entry.TimezoneConversions[1].Reason)
}

func TestOpenAIMCPMetadataCleanupHTTPWire(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "oauth"
		if passthrough {
			name = "oauth_passthrough"
		}
		t.Run(name, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			body := mcpMetadataCleanupBody(t, false)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 98)
			received := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				received <- raw
				response := openAICompatSSECompletedResponse("resp_mcp_metadata_cleanup", "gpt-5.4")
				defer response.Body.Close()
				for name, values := range response.Header {
					w.Header()[name] = values
				}
				w.WriteHeader(response.StatusCode)
				_, _ = io.Copy(w, response.Body)
			}))
			defer server.Close()
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			svc, _ := newOpenAIIdentityPathService(t, true, nil)
			svc.httpUpstream = &timezoneWireUpstream{client: server.Client(), target: target}
			account := newOpenAIIdentityPathOAuthAccount(1541)
			if passthrough {
				account.Extra = map[string]any{"openai_passthrough": true}
			}
			_, err = svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			select {
			case wire := <-received:
				requireMCPMetadataCleanupWire(t, body, wire)
			case <-time.After(time.Second):
				t.Fatal("no upstream HTTP request received")
			}
			requireMCPMetadataCleanupObservation(t, account.ID, "")
		})
	}
}

func TestOpenAIMCPMetadataCleanupWSWire(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			staged := newStagedPassthroughConn()
			cfg := passthroughLifecycleConfig()
			cfg.JWT.Secret = "mcp-metadata-local-ws-test"
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.PoolTargetUtilization = 1
			svc := newPassthroughLifecycleService(cfg, staged)
			dialer := &integrityWSDialer{traffic: staged}
			svc.openaiWSPassthroughDialer = dialer
			svc.openaiWSPool = newOpenAIWSConnPool(cfg)
			svc.openaiWSPool.setClientDialerForTest(dialer)
			defer svc.openaiWSPool.Close()
			account := newOpenAIIdentityPathOAuthAccount(1542)
			account.Status, account.Schedulable = StatusActive, true
			account.Extra = map[string]any{
				"responses_websockets_v2_enabled":           true,
				"openai_oauth_responses_websockets_v2_mode": mode,
				openAIPinnedInstallationIDKey:               transportTestPinnedInstallationID,
			}
			server, done := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer client.CloseNow()
			body := mcpMetadataCleanupBody(t, true)
			require.NoError(t, client.Write(ctx, coderws.MessageText, body))
			wire := requirePassthroughUpstreamWrite(t, staged, 3*time.Second)
			requireMCPMetadataCleanupWire(t, body, wire)
			staged.Send(`{"type":"response.completed","response":{"id":"resp_mcp_metadata_cleanup","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
			_, err = readPassthroughLifecycleFrame(t, client, 3*time.Second)
			require.NoError(t, err)
			require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("local WS gateway did not exit")
			}
			requireMCPMetadataCleanupObservation(t, account.ID, FingerprintObservationEventWSFrame)
		})
	}
}
