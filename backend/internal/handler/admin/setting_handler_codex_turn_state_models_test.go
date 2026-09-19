package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func codexTurnStateModelsResponse(t *testing.T, rec *httptest.ResponseRecorder) dto.SystemSettings {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data dto.SystemSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotContains(t, rec.Body.String(), service.SettingKeyCodexTurnStateModelsRevision)
	return body.Data
}

func TestSettingsCodexTurnStateModelsDefaultAndStoredEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored map[string]string
		want   []string
	}{
		{name: "missing uses defaults", stored: map[string]string{}, want: []string{"gpt-6-astra", "gpt-5.6-sol"}},
		{name: "explicit empty denies all", stored: map[string]string{service.SettingKeyCodexTurnStateModels: `[]`, service.SettingKeyCodexTurnStateModelsRevision: "hidden-revision"}, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newStepUpSwitchTestHandler(t, tc.stored)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
			h.GetSettings(c)
			settings := codexTurnStateModelsResponse(t, rec)
			require.Equal(t, tc.want, settings.CodexTurnStateModels)
			require.NotContains(t, rec.Body.String(), "hidden-revision")
		})
	}
}

func TestUpdateSettingsCodexTurnStateModelsNormalizesAndClears(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	rec := doUpdateSettings(t, h, map[string]any{
		service.SettingKeyCodexTurnStateModels: []string{" gpt-6-astra ", "gpt-6-astra", "Team/Model:V2", "team/model:v2"},
	}, nil)
	require.Equal(t, []string{"gpt-6-astra", "Team/Model:V2", "team/model:v2"}, codexTurnStateModelsResponse(t, rec).CodexTurnStateModels)
	require.JSONEq(t, `["gpt-6-astra","Team/Model:V2","team/model:v2"]`, repo.values[service.SettingKeyCodexTurnStateModels])
	require.NotEmpty(t, repo.values[service.SettingKeyCodexTurnStateModelsRevision])
	firstRevision := repo.values[service.SettingKeyCodexTurnStateModelsRevision]

	rec = doUpdateSettings(t, h, map[string]any{service.SettingKeyCodexTurnStateModels: []string{}}, nil)
	require.Equal(t, []string{}, codexTurnStateModelsResponse(t, rec).CodexTurnStateModels)
	require.Equal(t, `[]`, repo.values[service.SettingKeyCodexTurnStateModels])
	require.NotEqual(t, firstRevision, repo.values[service.SettingKeyCodexTurnStateModelsRevision])
}

func TestUpdateSettingsCodexTurnStateModelsOmittedPreservesPolicy(t *testing.T) {
	for _, stored := range []string{`["custom-model"]`, `[]`} {
		t.Run(stored, func(t *testing.T) {
			h, repo := newStepUpSwitchTestHandler(t, map[string]string{
				service.SettingKeyCodexTurnStateModels:         stored,
				service.SettingKeyCodexTurnStateModelsRevision: "existing-revision",
			})
			rec := doUpdateSettings(t, h, map[string]any{"site_name": "other settings"}, nil)
			settings := codexTurnStateModelsResponse(t, rec)
			var want []string
			require.NoError(t, json.Unmarshal([]byte(stored), &want))
			require.Equal(t, want, settings.CodexTurnStateModels)
			require.Equal(t, stored, repo.values[service.SettingKeyCodexTurnStateModels])
			require.Equal(t, "existing-revision", repo.values[service.SettingKeyCodexTurnStateModelsRevision])
			require.NotContains(t, repo.lastUpdates, service.SettingKeyCodexTurnStateModels)
			require.NotContains(t, repo.lastUpdates, service.SettingKeyCodexTurnStateModelsRevision)
		})
	}
}

func TestUpdateSettingsCodexTurnStateModelsRejectsInvalidWithoutWrites(t *testing.T) {
	tooMany := make([]string, service.CodexTurnStateModelsMaxCount+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("model-%d", i)
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "null", value: nil},
		{name: "string", value: "gpt-6-astra"},
		{name: "object", value: map[string]any{"model": "gpt-6-astra"}},
		{name: "non string item", value: []any{1}},
		{name: "null item", value: []any{nil}},
		{name: "empty ID", value: []string{""}},
		{name: "blank ID", value: []string{"  "}},
		{name: "wildcard", value: []string{"gpt-*"}},
		{name: "wildcard question", value: []string{"gpt-?"}},
		{name: "wildcard bracket", value: []string{"gpt-[6]"}},
		{name: "wildcard brace", value: []string{"gpt-{6}"}},
		{name: "control", value: []string{"gpt-\x00astra"}},
		{name: "leading control", value: []string{"\ngpt-6-astra"}},
		{name: "trailing control", value: []string{"gpt-6-astra\t"}},
		{name: "internal whitespace", value: []string{"gpt astra"}},
		{name: "too long", value: []string{strings.Repeat("x", service.CodexTurnStateModelMaxBytes+1)}},
		{name: "too many", value: tooMany},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newStepUpSwitchTestHandler(t, map[string]string{
				service.SettingKeyCodexTurnStateModels:         `["existing"]`,
				service.SettingKeyCodexTurnStateModelsRevision: "existing-revision",
			})
			rec := doUpdateSettings(t, h, map[string]any{service.SettingKeyCodexTurnStateModels: tc.value}, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Nil(t, repo.lastUpdates)
			require.Equal(t, `["existing"]`, repo.values[service.SettingKeyCodexTurnStateModels])
			require.Equal(t, "existing-revision", repo.values[service.SettingKeyCodexTurnStateModelsRevision])
		})
	}
}

func TestDiffSettingsIncludesCodexTurnStateModels(t *testing.T) {
	before := &service.SystemSettings{CodexTurnStateModels: []string{"gpt-6-astra"}}
	after := &service.SystemSettings{CodexTurnStateModels: []string{}}
	changed := diffSettings(before, after, nil, nil, UpdateSettingsRequest{})
	require.Contains(t, changed, service.SettingKeyCodexTurnStateModels)
	require.NotContains(t, diffSettings(before, before, nil, nil, UpdateSettingsRequest{}), service.SettingKeyCodexTurnStateModels)
}
