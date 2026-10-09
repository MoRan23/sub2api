package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupDaybreakPreferencesAreAdminOnly(t *testing.T) {
	group := &service.Group{ID: 1, OpenAIDaybreakBlueEnabled: true, OpenAIDaybreakRedEnabled: true}
	userJSON, err := json.Marshal(GroupFromService(group))
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "openai_daybreak")
	adminJSON, err := json.Marshal(GroupFromServiceAdmin(group))
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"openai_daybreak_blue_enabled":true`)
	require.Contains(t, string(adminJSON), `"openai_daybreak_red_enabled":true`)
}
