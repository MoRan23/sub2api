package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAIWSAccountFailoverReplayRequiresCompleteChainAndAllowsNewRoot(t *testing.T) {
	var replay openAIWSAccountFailoverReplay
	replay.Prepare([]byte(`{"input":[{"role":"user","content":"first"}]}`), "model-a", "", nil, nil)
	// Receiving a terminal without its output array is not proof that the
	// assistant's complete history is available, even if input is present.
	replay.Commit(&OpenAIForwardResult{RequestID: "resp_first"})
	replay.Prepare([]byte(`{"input":[{"role":"user","content":"continue"}]}`), "model-b", "resp_first", nil, nil)
	err := replay.Failover(&UpstreamFailoverError{StatusCode: 429}, OpenAIOAuthIdentityCapture{})
	payload, currentTurn := OpenAIWSCurrentTurnRetryPayload(err)
	require.True(t, currentTurn)
	require.Empty(t, payload)

	// A context-window reset supplies a new root. Old-window history must not
	// be appended, and unrelated large integers must retain their exact value.
	replay.Prepare([]byte(`{"previous_response_id":"resp_first","input":[{"role":"user","content":"new window"}],"extension":9007199254740993}`), "model-c", "", nil, nil)
	err = replay.Failover(&UpstreamFailoverError{StatusCode: 429}, OpenAIOAuthIdentityCapture{})
	payload, currentTurn = OpenAIWSCurrentTurnRetryPayload(err)
	require.True(t, currentTurn)
	require.Len(t, gjson.GetBytes(payload, "input").Array(), 1)
	require.Equal(t, "new window", gjson.GetBytes(payload, "input.0.content").String())
	require.False(t, gjson.GetBytes(payload, "previous_response_id").Exists())
	require.Equal(t, "model-c", gjson.GetBytes(payload, "model").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(payload, "extension").Raw)
}

func TestOpenAIWSAccountFailoverReplayRejectsDifferentResponseChain(t *testing.T) {
	var replay openAIWSAccountFailoverReplay
	replay.Prepare([]byte(`{"input":"first"}`), "model", "", nil, nil)
	replay.Commit(&OpenAIForwardResult{RequestID: "resp_first", wsAccountFailoverReplayComplete: true})
	replay.Prepare([]byte(`{"input":"other branch"}`), "model", "resp_other", nil, nil)
	payload, currentTurn := OpenAIWSCurrentTurnRetryPayload(replay.Failover(&UpstreamFailoverError{StatusCode: 429}, OpenAIOAuthIdentityCapture{}))
	require.True(t, currentTurn)
	require.Empty(t, payload, "history from a different response must not be guessed")
}

func TestOpenAIWSAccountFailoverReplayPreservesFrozenTimezoneAcrossRetry(t *testing.T) {
	var replay openAIWSAccountFailoverReplay
	body := timezoneWSBody(timezoneWSEnvironment)
	first, firstState := prepareTimezoneReplayTestState(t, body, time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC), openAIRequestSearchLocation())
	replay.Prepare(first, "model", "", firstState, nil)
	replay.Commit(&OpenAIForwardResult{
		RequestID: "resp_first", wsAccountFailoverReplayComplete: true,
		wsAccountFailoverReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first result"}]}`)},
	})
	second, secondState := prepareTimezoneReplayTestState(t, body, time.Date(2026, 1, 3, 0, 1, 0, 0, time.UTC), openAIRequestSearchLocation())
	var err error
	second, err = sjson.SetBytes(second, "previous_response_id", "resp_first")
	require.NoError(t, err)
	replay.Prepare(second, "model", "resp_first", secondState, nil)
	failover := replay.Failover(&UpstreamFailoverError{StatusCode: 429}, OpenAIOAuthIdentityCapture{})
	payload, currentTurn := OpenAIWSCurrentTurnRetryPayload(failover)
	require.True(t, currentTurn)
	state, ok := OpenAIWSCurrentTurnRetryTimezoneState(failover)
	require.True(t, ok)
	require.Equal(t, secondState.AcceptedAt, state.AcceptedAt)
	require.Len(t, gjson.GetBytes(payload, "input").Array(), 3)
	// Another account's timezone projection must preserve the first successful
	// turn's frozen date and the current turn's accepted clock independently.
	retargeted, _, ok := state.ProjectToTarget(payload, timezoneReplayTestTokyo())
	require.True(t, ok)
	require.Contains(t, gjson.GetBytes(retargeted, "input.0.content.0.text").String(), "<current_date>2026-01-01</current_date>")
	require.Contains(t, gjson.GetBytes(retargeted, "input.2.content.0.text").String(), "<current_date>2026-01-03</current_date>")
	require.Equal(t, "first result", gjson.GetBytes(retargeted, "input.1.content.0.text").String())
}
