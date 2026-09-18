package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const alphaSearchTimezoneRequest = `{
	"id":"timezone-search-session",
	"model":"gpt-5.6-sol",
	"commands":{"search_query":[{"q":"What time is it in Asia/Tokyo?"}],"time":[{"utc_offset":"+08:00"}]},
	"settings":{"user_location":{"type":"approximate","timezone":"Asia/Tokyo","country":"JP","city":"Tokyo"},"external_web_access":true,"search_context_size":"low","allowed_callers":["direct"]},
	"input":[{"role":"user","content":"Keep the query target Asia/Tokyo unchanged."}]
}`

func alphaSearchTimezoneAccount(route string, id int64) *Account {
	switch route {
	case "oauth":
		return newOpenAIIdentityPathOAuthAccount(id)
	case "apikey":
		return newOpenAIIdentityPathAPIKeyAccount(id)
	default:
		return newOpenAIIdentityPathPATAccount(id)
	}
}

func alphaSearchTimezoneResponse(route string) *http.Response {
	contentType, body := "application/json", `{"output":"search result"}`
	if route == "pat" {
		contentType, body = "text/event-stream", alphaSearchResponsesSSE("search result")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func requireAlphaSearchTimezoneWire(t *testing.T, route string, body []byte, timezone string) {
	t.Helper()
	expected := alphaSearchTimezoneRequest
	var err error
	if timezone == OpenAIRequestTimezone {
		expected, err = sjson.Set(alphaSearchTimezoneRequest, "settings.user_location", openAIRequestSearchLocation())
		locationPath := "settings.user_location"
		if route == "pat" {
			locationPath = "tools.0.user_location"
		}
		require.JSONEq(t, `{"type":"approximate","country":"US","region":"Washington","city":"Seattle","timezone":"America/Los_Angeles"}`, gjson.GetBytes(body, locationPath).Raw)
	}
	require.NoError(t, err)
	if route != "pat" {
		require.JSONEq(t, expected, string(body), "only the structured search location may change")
		return
	}
	require.JSONEq(t, gjson.Get(expected, "settings.user_location").Raw, gjson.GetBytes(body, "tools.0.user_location").Raw)
	require.Equal(t, "low", gjson.GetBytes(body, "tools.0.search_context_size").String())
	prompt := gjson.GetBytes(body, "input.0.content.0.text").String()
	commandsAndRest := strings.SplitN(prompt, "\nCommands JSON:\n", 2)
	require.Len(t, commandsAndRest, 2)
	commandsAndSettings := strings.SplitN(commandsAndRest[1], "\n\nSearch settings JSON:\n", 2)
	require.Len(t, commandsAndSettings, 2)
	require.JSONEq(t, gjson.Get(expected, "commands").Raw, commandsAndSettings[0], "time query offsets and search text are semantic inputs")
	settingsAndInput := strings.SplitN(commandsAndSettings[1], "\n\nRecent conversation/input JSON:\n", 2)
	require.Len(t, settingsAndInput, 2)
	require.JSONEq(t, gjson.Get(expected, "settings").Raw, settingsAndInput[0], "embedded settings must match the actual web_search tool location")
	require.JSONEq(t, gjson.Get(expected, "input").Raw, settingsAndInput[1], "conversation content must remain unchanged")
}

func requireAlphaSearchTimezoneObservation(t *testing.T, entry FingerprintObservationEntry, route, timezone string, enabled bool) {
	t.Helper()
	require.NotNil(t, entry.InboundTimezoneObservations)
	require.NotNil(t, entry.OutboundTimezoneObservations)
	require.Len(t, entry.InboundTimezoneObservations.Items, 1)
	require.Len(t, entry.OutboundTimezoneObservations.Items, 1)
	require.Equal(t, "settings.user_location.timezone", entry.InboundTimezoneObservations.Items[0].Path)
	require.Equal(t, "Asia/Tokyo", entry.InboundTimezoneObservations.Items[0].Value)
	path := "settings.user_location.timezone"
	if route == "pat" {
		path = "tools.0.user_location.timezone"
	}
	require.Equal(t, path, entry.OutboundTimezoneObservations.Items[0].Path)
	require.Equal(t, timezone, entry.OutboundTimezoneObservations.Items[0].Value)
	require.Equal(t, "matched", entry.TimezoneComparisonStatus, "conversion provenance must follow the actual endpoint body")
	if enabled {
		require.Len(t, entry.TimezoneConversions, 1)
		require.Equal(t, "converted", entry.TimezoneConversions[0].Status)
		require.Equal(t, "settings.user_location.timezone", entry.TimezoneConversions[0].Path)
	}
}

func TestForwardAlphaSearchTimezonePolicyAndObservation(t *testing.T) {
	for _, route := range []string{"oauth", "apikey", "pat"} {
		for _, enabled := range []bool{true, false} {
			for _, observe := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/convert=%t/observe=%t", route, enabled, observe), func(t *testing.T) {
					enableOpenAIIdentityPathFingerprintObservation(t)
					SetFingerprintObservationEnabled(observe)
					body := []byte(alphaSearchTimezoneRequest)
					c, _ := newOpenAIIdentityPathContext(t, "/v1/alpha/search", body, 61)
					policy := openai.DefaultRequestPolicy()
					policy.TimezoneConversionEnabled = enabled
					// Standalone search follows the normal policy, not the separately
					// configurable Responses passthrough policy.
					policy.PassthroughTimezoneConversionEnabled = !enabled
					c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), policy))
					upstream := &httpUpstreamRecorder{resp: alphaSearchTimezoneResponse(route)}
					service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := alphaSearchTimezoneAccount(route, 613)

					result, err := service.ForwardAlphaSearch(context.Background(), c, account, body)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.NotNil(t, upstream.lastReq)
					timezone := "Asia/Tokyo"
					if enabled {
						timezone = OpenAIRequestTimezone
					}
					requireAlphaSearchTimezoneWire(t, route, upstream.lastBody, timezone)
					require.Equal(t, alphaSearchTimezoneRequest, string(body), "the ingress body must remain reusable for account failover")
					entries := SnapshotFingerprintObservations(0)
					if observe {
						require.Len(t, entries, 1)
						requireAlphaSearchTimezoneObservation(t, entries[0], route, timezone, enabled)
					} else {
						require.Empty(t, entries)
					}
				})
			}
		}
	}
}

func TestForwardAlphaSearchTimezoneAccountFailoverResetsPathMapping(t *testing.T) {
	for _, firstRoute := range []string{"oauth", "pat"} {
		t.Run(firstRoute, func(t *testing.T) {
			enableOpenAIIdentityPathFingerprintObservation(t)
			body := []byte(alphaSearchTimezoneRequest)
			c, _ := newOpenAIIdentityPathContext(t, "/v1/alpha/search", body, 62)
			policy := openai.DefaultRequestPolicy()
			policy.TimezoneConversionEnabled = true
			c.Request = c.Request.WithContext(openai.WithRequestPolicy(c.Request.Context(), policy))
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unauthorized"}}`)),
			}}
			service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			firstAccount := alphaSearchTimezoneAccount(firstRoute, 614)
			result, err := service.ForwardAlphaSearch(context.Background(), c, firstAccount, body)
			require.Nil(t, result)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.False(t, c.Writer.Written())
			requireAlphaSearchTimezoneWire(t, firstRoute, upstream.lastBody, OpenAIRequestTimezone)

			secondRoute := "pat"
			if firstRoute == "pat" {
				secondRoute = "oauth"
			}
			secondAccount := alphaSearchTimezoneAccount(secondRoute, 615)
			upstream.resp = alphaSearchTimezoneResponse(secondRoute)
			result, err = service.ForwardAlphaSearch(context.Background(), c, secondAccount, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			requireAlphaSearchTimezoneWire(t, secondRoute, upstream.lastBody, OpenAIRequestTimezone)
			entries := SnapshotFingerprintObservations(0)
			require.Len(t, entries, 2)
			for _, entry := range entries {
				route := firstRoute
				if entry.AccountID == secondAccount.ID {
					route = secondRoute
				}
				requireAlphaSearchTimezoneObservation(t, entry, route, OpenAIRequestTimezone, true)
			}
		})
	}
}
