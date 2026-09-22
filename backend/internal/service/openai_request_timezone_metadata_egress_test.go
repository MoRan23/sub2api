package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A real client may attach unrelated internal metadata without declaring any
// content_item_kinds. Freeze structural eligibility before that metadata is
// stripped, then apply the actual account exit's timezone at the send boundary.
func TestOpenAIRequestTimezoneUnlabelledMetadataUsesActualEgress(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, observe := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%t/observe=%t", passthrough, observe), func(t *testing.T) {
				enableOpenAIIdentityPathFingerprintObservation(t)
				SetFingerprintObservationEnabled(observe)
				resolver := NewOpenAIEgressLocationService(nil)
				t.Cleanup(resolver.Stop)
				account := egressTimezoneAccount(t, resolver, 91, "Asia/Tokyo", "Japan", "Tokyo", "Tokyo")
				if passthrough {
					account.Extra = map[string]any{"openai_passthrough": true}
				}
				environment := timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")
				body := timezoneTestBody(t, map[string]any{
					"model": "gpt-5.4", "stream": false,
					"input": []any{map[string]any{
						"type": "message", "role": "user",
						"content": []any{
							map[string]any{"type": "input_text", "text": "Keep this ordinary message unchanged."},
							map[string]any{"type": "input_text", "text": environment},
						},
						"internal_chat_message_metadata_passthrough": map[string]any{"executed_tool_calls": []any{}},
					}},
					"tools": []any{map[string]any{"type": "web_search"}},
				})
				c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 91)
				c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), timezoneTestPolicy()))
				response := successfulInstallationTestResponse()
				if passthrough {
					response = openAICompatSSECompletedResponse("resp_egress_metadata", "gpt-5.4")
				}
				upstream := &httpUpstreamRecorder{resp: response}
				svc, _ := newOpenAIIdentityPathService(t, false, upstream)
				svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
				svc.egressLocationService = resolver
				svc.CaptureOpenAIRequestTimezone(c, body)
				capture, _ := c.Get(openAIRequestTimezoneCaptureKey)
				capture.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()

				_, err := svc.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
				require.Equal(t, timezoneTestEnvironment("Asia/Tokyo", "2026-09-10"), gjson.GetBytes(upstream.lastBody, "input.0.content.1.text").String())
				require.Equal(t, "Keep this ordinary message unchanged.", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
				require.Equal(t, "Tokyo", gjson.GetBytes(upstream.lastBody, "tools.0.user_location.city").String())
				require.Equal(t, "Asia/Tokyo", gjson.GetBytes(upstream.lastBody, "tools.0.user_location.timezone").String())
				require.False(t, gjson.GetBytes(upstream.lastBody, "input.0.internal_chat_message_metadata_passthrough").Exists())
				require.Equal(t, environment, gjson.GetBytes(body, "input.0.content.1.text").String(), "ingress stays immutable")

				state, ok := RequestTimezoneStateFromContext(c)
				require.True(t, ok)
				entries := SnapshotFingerprintObservations(0)
				if !observe {
					require.Empty(t, entries)
					return
				}
				require.NotNil(t, state.Inbound)
				require.Equal(t, "structural_fallback", state.Inbound.Items[0].EnvironmentSource)
				require.Len(t, entries, 1)
				entry := entries[0]
				require.Equal(t, "matched", entry.TimezoneComparisonStatus)
				require.Equal(t, "structural_fallback", entry.OutboundTimezoneObservations.Items[0].EnvironmentSource)
				require.Equal(t, "Asia/Tokyo", entry.OutboundTimezoneObservations.Items[0].Value)
				require.NotNil(t, entry.RequestIntegrity)
				require.Equal(t, "expected_transform", entry.RequestIntegrity.Status)
				require.Empty(t, entry.RequestIntegrity.ChangedFields)
				require.Contains(t, entry.RequestIntegrity.RuleCodes, "frozen_timezone_patch")
			})
		}
	}
}
