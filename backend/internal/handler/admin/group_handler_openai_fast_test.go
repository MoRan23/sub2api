package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequestsDecodeForceOpenAIFast(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"fast","force_openai_fast":true,"free_openai_fast":true}`), &createReq))
	require.True(t, createReq.ForceOpenAIFast)
	require.True(t, createReq.FreeOpenAIFast)

	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"force_openai_fast":false,"free_openai_fast":false}`), &updateReq))
	require.NotNil(t, updateReq.ForceOpenAIFast)
	require.False(t, *updateReq.ForceOpenAIFast)
	require.NotNil(t, updateReq.FreeOpenAIFast)
	require.False(t, *updateReq.FreeOpenAIFast)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ForceOpenAIFast)
	require.Nil(t, omitted.FreeOpenAIFast)
}

func TestGroupRequestsDecodeUltrafast(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"ultrafast","force_openai_ultrafast":true,"free_openai_ultrafast":true}`), &createReq))
	require.True(t, createReq.ForceOpenAIUltrafast)
	require.True(t, createReq.FreeOpenAIUltrafast)
	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"force_openai_ultrafast":false,"free_openai_ultrafast":false}`), &updateReq))
	require.NotNil(t, updateReq.ForceOpenAIUltrafast)
	require.False(t, *updateReq.ForceOpenAIUltrafast)
	require.NotNil(t, updateReq.FreeOpenAIUltrafast)
	require.False(t, *updateReq.FreeOpenAIUltrafast)
	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ForceOpenAIUltrafast)
	require.Nil(t, omitted.FreeOpenAIUltrafast)
	require.Error(t, json.Unmarshal([]byte(`{"force_openai_ultrafast":"true"}`), &omitted))
	require.Error(t, json.Unmarshal([]byte(`{"free_openai_ultrafast":1}`), &omitted))
}
