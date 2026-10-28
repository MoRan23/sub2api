package openai_ws_v2

import (
	"context"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestRelay_InterruptAcknowledgementsDoNotCreateOrSettleTurns(t *testing.T) {
	for _, ackFirst := range []bool{true, false} {
		for _, kind := range []string{"response.interrupt.accepted", "response.interrupt.failed"} {
			t.Run(kind+map[bool]string{true: "_before", false: "_after"}[ackFirst], func(t *testing.T) {
				created := `{"type":"response.created","response":{"id":"resp_control"}}`
				completed := `{"type":"response.completed","response":{"id":"resp_control","usage":{"input_tokens":7,"output_tokens":3}}}`
				ack := `{"type":"` + kind + `","response_id":"resp_control"}`
				payloads := []string{created, completed, ack}
				if ackFirst {
					payloads = []string{created, ack, completed}
				}
				frames := make([]passthroughTestFrame, 0, len(payloads))
				for _, payload := range payloads {
					frames = append(frames, passthroughTestFrame{msgType: coderws.MessageText, payload: []byte(payload)})
				}
				client := newPassthroughTestFrameConn(nil, false)
				upstream := newPassthroughTestFrameConn(frames, true)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				var turns []RelayTurnResult
				result, exit := Relay(ctx, client, upstream, []byte(`{"type":"response.create","model":"gpt-test"}`), RelayOptions{OnTurnComplete: func(turn RelayTurnResult) { turns = append(turns, turn) }})
				require.Nil(t, exit)
				require.Len(t, turns, 1)
				require.Equal(t, 7, result.Usage.InputTokens)
				require.Equal(t, 3, result.Usage.OutputTokens)
				writes := client.Writes()
				require.Len(t, writes, len(payloads))
				for i := range payloads {
					require.Equal(t, payloads[i], string(writes[i].payload))
				}
			})
		}
	}
}

func TestObserveInterruptAckPreservesNextTurnStart(t *testing.T) {
	state := &relayState{}
	now := time.Now()
	state.setPendingTurnStartedAt(now.Add(-time.Second))
	observeUpstreamMessage(state, []byte(`{"type":"response.interrupt.accepted","response_id":"resp_old"}`), now, func() time.Time { return now }, nil)
	require.Nil(t, state.activeTurn)
	observeUpstreamMessage(state, []byte(`{"type":"response.created","response":{"id":"resp_new"}}`), now, func() time.Time { return now }, nil)
	require.Equal(t, now.Add(-time.Second), state.activeTurn.startAt)
	require.NotContains(t, state.turnTimingByID, "resp_old")
}
