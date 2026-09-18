package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func egressTimezoneAccount(t *testing.T, resolver *OpenAIEgressLocationService, id int64, zone, country, region, city string) *Account {
	t.Helper()
	account := newOpenAIIdentityPathOAuthAccount(id)
	proxy := &Proxy{ID: id, Protocol: "http", Host: "proxy.example", Port: int(8000 + id)}
	account.ProxyID, account.Proxy = &proxy.ID, proxy
	resolver.ObserveResult(OpenAIEgressRoute{ProxyID: id, ProxyURL: proxy.URL()}, &ProxyExitInfo{IP: "203.0.113.5", Country: country, CountryCode: "JP", Region: region, City: city, Timezone: zone, GeoStatus: "success", GeoCheckedAt: time.Now()}, nil)
	return account
}

func TestOpenAIRequestTimezoneEgressPhysicalHTTPPaths(t *testing.T) {
	for _, route := range []string{"responses", "passthrough", "chat", "chat_string", "messages", "alpha", "pat"} {
		for _, observe := range []bool{false, true} {
			t.Run(route+map[bool]string{true: "/observe", false: "/no_observe"}[observe], func(t *testing.T) {
				enableOpenAIIdentityPathFingerprintObservation(t)
				SetFingerprintObservationEnabled(observe)
				resolver := NewOpenAIEgressLocationService(nil)
				t.Cleanup(resolver.Stop)
				account := egressTimezoneAccount(t, resolver, 91, "Asia/Tokyo", "Japan", "Tokyo", "Tokyo")
				env := timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")
				payload := map[string]any{"model": "gpt-5.4", "stream": false, "tools": []any{map[string]any{"type": "web_search"}}, "input": timezoneTestInput(env)}
				path := "/v1/responses"
				response := successfulInstallationTestResponse()
				if strings.HasPrefix(route, "chat") || route == "messages" {
					delete(payload, "input")
					payload["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": env}}}}
					if route == "chat_string" {
						payload["messages"] = []any{map[string]any{"role": "user", "content": env}}
					}
					path = "/v1/chat/completions"
					response = openAICompatSSECompletedResponse("resp_egress", "gpt-5.4")
					if route == "messages" {
						payload["max_tokens"] = 32
						path = "/v1/messages"
					}
				}
				if route == "passthrough" {
					account.Extra = map[string]any{"openai_passthrough": true}
					response = openAICompatSSECompletedResponse("resp_egress_passthrough", "gpt-5.4")
				}
				body := timezoneTestBody(t, payload)
				if route == "alpha" || route == "pat" {
					body, path = []byte(alphaSearchTimezoneRequest), "/v1/alpha/search"
					response = alphaSearchTimezoneResponse("oauth")
					if route == "pat" {
						pat := newOpenAIIdentityPathPATAccount(account.ID)
						pat.Proxy, pat.ProxyID = account.Proxy, account.ProxyID
						account = pat
						response = alphaSearchTimezoneResponse("pat")
					}
				}
				c, _ := newOpenAIIdentityPathContext(t, path, body, 91)
				c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), timezoneTestPolicy()))
				upstream := &httpUpstreamRecorder{resp: response}
				svc, _ := newOpenAIIdentityPathService(t, false, upstream)
				svc.egressLocationService = resolver
				svc.CaptureOpenAIRequestTimezone(c, body)
				capture, _ := c.Get(openAIRequestTimezoneCaptureKey)
				capture.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()
				var err error
				switch route {
				case "chat", "chat_string":
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "egress", "")
				case "messages":
					_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "egress", "")
				case "alpha", "pat":
					_, err = svc.ForwardAlphaSearch(context.Background(), c, account, body)
				default:
					_, err = svc.Forward(context.Background(), c, account, body)
				}
				require.NoError(t, err)
				require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
				locationPath := "tools.0.user_location"
				if route == "alpha" {
					locationPath = "settings.user_location"
				}
				require.Equal(t, "Tokyo", gjson.GetBytes(upstream.lastBody, locationPath+".city").String())
				require.Equal(t, "Asia/Tokyo", gjson.GetBytes(upstream.lastBody, locationPath+".timezone").String())
				if route != "alpha" && route != "pat" {
					scan := ScanOpenAIRequestTimezones(upstream.lastBody)
					require.Equal(t, "Asia/Tokyo", scan.Items[0].Value)
					require.Equal(t, "2026-09-10", scan.Items[0].CurrentDate)
				} else if route == "pat" {
					prompt := gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String()
					require.Contains(t, prompt, `"city":"Tokyo"`)
					require.Contains(t, prompt, `"utc_offset":"+08:00"`)
				}
				state, ok := RequestTimezoneStateFromContext(c)
				require.True(t, ok)
				require.Equal(t, "Tokyo", state.EgressLocation.City)
				if strings.HasPrefix(route, "chat") || route == "messages" {
					integrity := openAIIntegrityCaptureFromContext(c)
					require.NotNil(t, integrity)
					require.NotNil(t, integrity.state)
					require.NotContains(t, string(integrity.state.baseline), `"city":"Tokyo"`)
				}
				entries := SnapshotFingerprintObservations(0)
				if !observe {
					require.Empty(t, entries)
				} else {
					require.NotEmpty(t, entries)
					require.Equal(t, "Asia/Tokyo", entries[0].TimezoneTarget)
					require.Equal(t, "Tokyo", entries[0].EgressLocation.City)
					require.Equal(t, "matched", entries[0].TimezoneComparisonStatus)
					if strings.HasPrefix(route, "chat") || route == "messages" {
						require.NotNil(t, entries[0].RequestIntegrity)
						require.Equal(t, "expected_transform", entries[0].RequestIntegrity.Status, "exact route patches must explain the physical request")
						require.Empty(t, entries[0].RequestIntegrity.ChangedFields)
						require.Contains(t, entries[0].RequestIntegrity.RuleCodes, "frozen_timezone_patch")
					}
				}
			})
		}
	}
}

func TestOpenAIRequestTimezoneEgressFailoverUsesOriginalSource(t *testing.T) {
	for _, route := range []string{"responses", "chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			SetFingerprintObservationEnabled(false)
			t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
			resolver := NewOpenAIEgressLocationService(nil)
			t.Cleanup(resolver.Stop)
			first := egressTimezoneAccount(t, resolver, 92, "America/Los_Angeles", "United States", "Washington", "Seattle")
			second := egressTimezoneAccount(t, resolver, 93, "Asia/Tokyo", "Japan", "Tokyo", "Tokyo")
			payload := map[string]any{"model": "gpt-5.4", "stream": false, "tools": []any{map[string]any{"type": "web_search"}}, "input": timezoneTestInput(timezoneTestEnvironment("Asia/Shanghai", "2026-09-10"))}
			if route != "responses" {
				delete(payload, "input")
				payload["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")}}}}
				payload["max_tokens"] = 32
			}
			body := timezoneTestBody(t, payload)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 92)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized"}}`))}}
			svc, _ := newOpenAIIdentityPathService(t, false, upstream)
			svc.egressLocationService = resolver
			svc.CaptureOpenAIRequestTimezone(c, body)
			capture, _ := c.Get(openAIRequestTimezoneCaptureKey)
			capture.(*openAIRequestTimezoneCapture).acceptedAt = timezoneTestAcceptedAt()
			send := func(account *Account) error {
				var err error
				switch route {
				case "chat":
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "egress", "")
				case "messages":
					_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "egress", "")
				default:
					_, err = svc.Forward(context.Background(), c, account, body)
				}
				return err
			}
			require.Error(t, send(first))
			require.Equal(t, "Seattle", gjson.GetBytes(upstream.lastBody, "tools.0.user_location.city").String())
			require.Equal(t, "2026-09-09", ScanOpenAIRequestTimezones(upstream.lastBody).Items[0].CurrentDate)
			upstream.resp = successfulInstallationTestResponse()
			if route != "responses" {
				upstream.resp = openAICompatSSECompletedResponse("resp_failover", "gpt-5.4")
			}
			require.NoError(t, send(second))
			require.Equal(t, second.Proxy.URL(), upstream.lastProxyURL)
			require.Equal(t, "Tokyo", gjson.GetBytes(upstream.lastBody, "tools.0.user_location.city").String())
			require.Equal(t, "2026-09-10", ScanOpenAIRequestTimezones(upstream.lastBody).Items[0].CurrentDate)
		})
	}
}

func TestOpenAIRequestTimezoneEgressSameRouteFrozenAndAPIKeyUnchanged(t *testing.T) {
	resolver := NewOpenAIEgressLocationService(nil)
	t.Cleanup(resolver.Stop)
	account := newOpenAIIdentityPathOAuthAccount(94)
	body := []byte(`{"tools":[{"type":"web_search"}]}`)
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 94)
	svc := &OpenAIGatewayService{egressLocationService: resolver}
	first := svc.prepareOpenAIRequestTimezone(context.Background(), c, account, body, false)
	require.Equal(t, "Seattle", gjson.GetBytes(first, "tools.0.user_location.city").String())
	resolver.ObserveResult(OpenAIEgressRoute{}, egressLocationTestInfo("203.0.113.1", time.Now()), nil)
	second := svc.prepareOpenAIRequestTimezone(context.Background(), c, account, body, false)
	require.JSONEq(t, string(first), string(second), "background refresh cannot change this turn's fallback")
	ResetOpenAIRequestTimezoneTargets(c)
	third := svc.prepareOpenAIRequestTimezone(context.Background(), c, account, body, false)
	require.Equal(t, "Berlin", gjson.GetBytes(third, "tools.0.user_location.city").String())
	apiKey := newOpenAIIdentityPathAPIKeyAccount(95)
	out := svc.prepareOpenAIRequestTimezone(context.Background(), c, apiKey, body, false)
	require.Equal(t, "Seattle", gjson.GetBytes(out, "tools.0.user_location.city").String())
	state, _ := RequestTimezoneStateFromContext(c)
	require.Nil(t, state.EgressLocation)
	encoded, err := json.Marshal(state.Target)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "proxy")
}

func TestOpenAIRequestTimezoneEgressCompatibilityRetainsNegativeMetadata(t *testing.T) {
	for _, route := range []string{"chat", "chat_string", "messages"} {
		for _, metadata := range []any{nil, map[string]any{"content_item_kinds": []any{"ordinary_text"}}, map[string]any{"content_item_kinds": []any{123}}} {
			for _, markerPath := range []string{"messages.0.internal_chat_message_metadata_passthrough", "messages.0.content.0.internal_chat_message_metadata_passthrough", "internal_chat_message_metadata_passthrough", "content_item_kinds", "metadata.content_item_kinds"} {
				if route == "chat_string" && strings.HasPrefix(markerPath, "messages.0.content.") {
					continue
				}
				t.Run(route+"/"+markerPath+"/"+string(timezoneTestBody(t, metadata)), func(t *testing.T) {
					SetFingerprintObservationEnabled(false)
					t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
					resolver := NewOpenAIEgressLocationService(nil)
					t.Cleanup(resolver.Stop)
					account := egressTimezoneAccount(t, resolver, 96, "Asia/Tokyo", "Japan", "Tokyo", "Tokyo")
					env := timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")
					body := timezoneTestBody(t, map[string]any{"model": "gpt-5.4", "stream": false, "max_tokens": 32,
						"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": env}}}},
						"tools":    []any{map[string]any{"type": "web_search"}}})
					if route == "chat_string" {
						body, _ = sjson.SetBytes(body, "messages.0.content", env)
					}
					body, err := sjson.SetBytes(body, markerPath, metadata)
					require.NoError(t, err)
					c, _ := newOpenAIIdentityPathContext(t, "/v1/chat/completions", body, 96)
					upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_negative", "gpt-5.4")}
					svc, _ := newOpenAIIdentityPathService(t, false, upstream)
					svc.egressLocationService = resolver
					if strings.HasPrefix(route, "chat") {
						_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "egress", "")
					} else {
						_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "egress", "")
					}
					if route == "chat" && markerPath == "messages.0.content.0.internal_chat_message_metadata_passthrough" && metadata != nil {
						require.ErrorContains(t, err, "unsupported_content_part_field")
						require.Nil(t, upstream.lastReq, "existing semantic validator rejects unsupported nonempty part metadata before sending")
						return
					}
					require.NoError(t, err)
					require.Equal(t, "Asia/Shanghai", ScanOpenAIRequestTimezones(upstream.lastBody).Items[0].Value, "dropping an explicit wrong/null marker must not authorize strict fallback")
					require.Equal(t, "Tokyo", gjson.GetBytes(upstream.lastBody, "tools.0.user_location.city").String(), "an unrelated search location still uses the actual route")
				})
			}
		}
	}
}

func TestOpenAIRequestTimezoneEgressDisabledStillObservesActualWire(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	resolver := NewOpenAIEgressLocationService(nil)
	t.Cleanup(resolver.Stop)
	account := egressTimezoneAccount(t, resolver, 97, "Asia/Tokyo", "Japan", "Tokyo", "Tokyo")
	body := timezoneTestBody(t, map[string]any{"model": "gpt-5.4", "stream": false, "input": timezoneTestInput(timezoneTestEnvironment("Asia/Shanghai", "2026-09-10")), "tools": []any{map[string]any{"type": "web_search", "user_location": map[string]any{"city": "Paris", "timezone": "Europe/Paris"}}}})
	c, _ := newOpenAIIdentityPathContext(t, "/v1/responses", body, 97)
	policy := timezoneTestPolicy()
	policy.TimezoneConversionEnabled = false
	c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), policy))
	upstream := &httpUpstreamRecorder{resp: successfulInstallationTestResponse()}
	svc, _ := newOpenAIIdentityPathService(t, false, upstream)
	svc.egressLocationService = resolver
	_, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, "Paris", gjson.GetBytes(upstream.lastBody, "tools.0.user_location.city").String())
	require.Equal(t, "Asia/Shanghai", ScanOpenAIRequestTimezones(upstream.lastBody).Items[0].Value)
	entries := SnapshotFingerprintObservations(0)
	require.Len(t, entries, 1)
	require.Equal(t, "Asia/Tokyo", entries[0].TimezoneTarget)
	require.Equal(t, "Tokyo", entries[0].EgressLocation.City)
	require.Equal(t, "Paris", entries[0].OutboundTimezoneObservations.Items[1].Location.City, "egress target must not populate actual observations")
	require.Equal(t, "disabled", entries[0].TimezoneConversions[0].Status)
}

func TestOpenAIRequestTimezoneProjectionSnapshotsAndAbsentContainer(t *testing.T) {
	body := []byte(`{"commands":{"search_query":[{"q":"sample"}]}}`)
	prepared, state := prepareOpenAIRequestTimezoneBody(body, timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true, true)
	neutral, ok := state.UndoToBody(prepared)
	require.True(t, ok)
	require.JSONEq(t, string(body), string(neutral), "undo restores absence of an automatically added settings container")
	tokyo := RequestLocationObservation{Type: "approximate", Country: "JP", Region: "Tokyo", City: "Tokyo", Timezone: "Asia/Tokyo"}
	projected, next, ok := state.ProjectToTarget(prepared, tokyo)
	require.True(t, ok)
	require.Equal(t, "Tokyo", gjson.GetBytes(projected, "settings.user_location.city").String())
	require.Equal(t, openAIRequestSearchLocation(), state.Target)
	require.Equal(t, tokyo, next.Target)
	_, source := PrepareOpenAIRequestTimezone([]byte(`{"tools":[{"type":"web_search","user_location":{"city":"Paris","timezone":"Europe/Paris"}}]}`), timezoneTestPolicy(), timezoneTestAcceptedAt(), false, true)
	source.Inbound.Items[0].Location.City = "mutated observer"
	clone := source.WithTarget(tokyo)
	require.Equal(t, "Paris", clone.Conversions[0].LocationBefore.City, "mutable observations do not change frozen semantic preimages")
	source.EgressLocation = &OpenAIEgressLocationSnapshot{City: "Seattle"}
	entry := cloneFingerprintObservationEntry(FingerprintObservationEntry{EgressLocation: source.EgressLocation})
	entry.EgressLocation.City = "edited UI snapshot"
	require.Equal(t, "Seattle", source.EgressLocation.City)
}
