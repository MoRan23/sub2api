package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexWireProfile0165TypedMetadataFixture(t *testing.T) {
	// Field order and types follow Codex 0.165 core/src/responses_metadata.rs.
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_0165", "typed_turn_metadata.golden.json"))
	require.NoError(t, err)
	fixture := strings.TrimSpace(string(raw))
	profile := ParseCodexWireProfile(fixture)
	require.Equal(t, CodexHistoryInitializationWarmFork, profile.HistoryInitialization)
	require.Equal(t, boolPointer(true), profile.GuardianRecordedItems)
	require.Equal(t, boolPointer(false), profile.AnalyticsEnabled)
	require.Empty(t, profile.ExtraMetadata)
	for _, body := range []bool{false, true} {
		projected, err := profile.MarshalNestedJSON(body)
		require.NoError(t, err)
		require.Equal(t, fixture, projected)
	}
}

func TestCodexWireProfile0165TypedMetadataValues(t *testing.T) {
	for _, history := range []string{"new", "cleared", "cold_resume", "warm_fork", "cold_fork", "supplied_history"} {
		for _, value := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", history, value), func(t *testing.T) {
				raw := fmt.Sprintf(`{"history_initialization":%q,"guardian_recorded_items":%t,"analytics_enabled":%t}`, history, value, value)
				profile := ParseCodexWireProfile(raw).withDefaults(CodexMetadataProfile{})
				for _, includeTools := range []bool{false, true} {
					projected, err := profile.MarshalNestedJSON(includeTools)
					require.NoError(t, err)
					require.Equal(t, history, gjson.Get(projected, "history_initialization").String())
					for _, key := range []string{"guardian_recorded_items", "analytics_enabled"} {
						require.Equal(t, fmt.Sprint(value), gjson.Get(projected, key).Raw)
					}
				}
			})
		}
	}
	for _, raw := range []string{
		`{}`, `{"agent_name":"guardian","thread_source":"guardian_classifier","subagent_kind":"guardian"}`,
		`{"history_initialization":"invalid","guardian_recorded_items":"false","analytics_enabled":"true"}`,
		`{"history_initialization":" new ","guardian_recorded_items":null,"analytics_enabled":1}`,
	} {
		profile := ParseCodexWireProfile(raw).withDefaults(CodexMetadataProfile{})
		projected, err := profile.MarshalNestedJSON(true)
		require.NoError(t, err)
		for _, key := range []string{"history_initialization", "guardian_recorded_items", "analytics_enabled"} {
			require.False(t, gjson.Get(projected, key).Exists(), raw)
			require.NotContains(t, profile.ExtraMetadata, key)
		}
	}
}

func TestCodexWireProfile0165CaptureAndSnapshot(t *testing.T) {
	canonical := `{"history_initialization":"cold_resume","guardian_recorded_items":false,"analytics_enabled":false}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/responses", nil)
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"history_initialization":"new","guardian_recorded_items":true,"analytics_enabled":true}`)
	profile := captureCodexWireProfile(c, codexWireTestBody(t, canonical, map[string]any{
		"history_initialization": "cold_fork", "guardian_recorded_items": true, "analytics_enabled": true,
	}), "")
	require.Equal(t, CodexHistoryInitializationColdResume, profile.HistoryInitialization)
	require.Equal(t, boolPointer(false), profile.GuardianRecordedItems)
	require.Equal(t, boolPointer(false), profile.AnalyticsEnabled)
	clone := cloneCodexWireProfile(profile)
	*profile.GuardianRecordedItems, *profile.AnalyticsEnabled = true, true
	require.Equal(t, boolPointer(false), clone.GuardianRecordedItems)
	require.Equal(t, boolPointer(false), clone.AnalyticsEnabled)
	merged := newCodexWireProfile()
	mergeCodexWireProfileMissing(&merged, clone)
	*clone.GuardianRecordedItems, *clone.AnalyticsEnabled = true, true
	require.Equal(t, boolPointer(false), merged.GuardianRecordedItems)
	require.Equal(t, boolPointer(false), merged.AnalyticsEnabled)
	require.Equal(t, CodexHistoryInitializationColdResume, merged.HistoryInitialization)
}

func TestCodexWireProfile0165HTTPAndWSProjection(t *testing.T) {
	for _, test := range []struct {
		mode      OpenAIOAuthIdentityProjectionMode
		transport string
	}{
		{OpenAIOAuthIdentityProjectionRegular, "http"},
		{OpenAIOAuthIdentityProjectionRegular, "ws"},
		{OpenAIOAuthIdentityProjectionPassthrough, "http"},
		{OpenAIOAuthIdentityProjectionPassthrough, "ws"},
		{OpenAIOAuthIdentityProjectionCompact, "http"},
	} {
		t.Run(string(test.mode)+"/"+test.transport, func(t *testing.T) {
			plan := codexWireProjectionTestPlan(t)
			plan.ProjectionMode = test.mode
			plan.WireProfile.HistoryInitialization = CodexHistoryInitializationSuppliedHistory
			plan.WireProfile.GuardianRecordedItems = boolPointer(false)
			plan.WireProfile.AnalyticsEnabled = boolPointer(true)
			kind := CodexWireRequestTurn
			if test.mode == OpenAIOAuthIdentityProjectionCompact {
				kind = CodexWireRequestCompaction
			}
			finalPlan, err := FinalizeOpenAICodexWirePlan(plan, string(kind), CodexModelCapabilities{})
			require.NoError(t, err)
			attribution := `{"status":"complete","sources":[{"server_name":"private-server","tool_name":"private-tool","first_turn_id":"private-turn"}]}`
			request := map[string]any{
				"model": "gpt-5.4", "input": []any{},
				"client_metadata": map[string]any{
					"history_initialization": "cold_fork", "guardian_recorded_items": true, "analytics_enabled": false,
					"mcp_attribution":          attribution,
					openAIWSTurnMetadataHeader: `{"history_initialization":"new","guardian_recorded_items":true,"analytics_enabled":false,"mcp_attribution":"header-leak"}`,
				},
			}
			if test.mode != OpenAIOAuthIdentityProjectionCompact {
				request["history_initialization"], request["guardian_recorded_items"], request["analytics_enabled"] = "new", true, false
			}
			if test.transport == "ws" {
				request["type"] = "response.create"
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			headers := http.Header{openAIWSTurnMetadataHeader: {`{"mcp_attribution":"header-leak"}`}}
			out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, finalPlan)
			require.NoError(t, err)
			nestedCarriers := []string{headers.Get(openAIWSTurnMetadataHeader)}
			if test.mode == OpenAIOAuthIdentityProjectionCompact {
				// Compact carries typed metadata only in the header; the complete
				// client_metadata container, including MCP attribution, is excluded.
				require.False(t, gjson.GetBytes(out, "client_metadata").Exists())
				require.Equal(t, "[]", gjson.GetBytes(out, "input").Raw)
			} else {
				nestedCarriers = append(nestedCarriers, gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String())
				require.Equal(t, attribution, gjson.GetBytes(out, "client_metadata.mcp_attribution").String())
			}
			for _, nested := range nestedCarriers {
				require.Equal(t, string(kind), gjson.Get(nested, "request_kind").String())
				require.Equal(t, "supplied_history", gjson.Get(nested, "history_initialization").String())
				require.Equal(t, "false", gjson.Get(nested, "guardian_recorded_items").Raw)
				require.Equal(t, "true", gjson.Get(nested, "analytics_enabled").Raw)
				require.False(t, gjson.Get(nested, "mcp_attribution").Exists())
			}
			for _, key := range []string{"history_initialization", "guardian_recorded_items", "analytics_enabled"} {
				require.False(t, gjson.GetBytes(out, key).Exists())
				require.False(t, gjson.GetBytes(out, "client_metadata."+key).Exists())
			}
		})
	}
}
