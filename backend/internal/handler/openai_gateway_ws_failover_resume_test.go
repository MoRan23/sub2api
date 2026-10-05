package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSNextAttemptMessageUsesCurrentTurnPayload(t *testing.T) {
	firstMessage := []byte(`{"type":"response.create","input":"first"}`)
	currentTurn := []byte(`{"type":"response.create","input":"turn-281"}`)

	next, ok := openAIWSNextAttemptMessage(firstMessage, currentTurn, true, true)

	require.True(t, ok)
	require.Equal(t, currentTurn, next)
	next[0] = 'x'
	require.Equal(t, byte('{'), currentTurn[0], "retry payload must be cloned")
}

func TestOpenAIWSNextAttemptMessageRejectsMissingCurrentTurnPayload(t *testing.T) {
	next, ok := openAIWSNextAttemptMessage([]byte(`{"type":"response.create"}`), nil, true, true)

	require.False(t, ok)
	require.Nil(t, next)
}

func TestOpenAIWSNextAttemptMessageKeepsInitialMessageForFirstTurnFailover(t *testing.T) {
	firstMessage := []byte(`{"type":"response.create","input":"first"}`)

	next, ok := openAIWSNextAttemptMessage(firstMessage, nil, false, false)

	require.True(t, ok)
	require.Equal(t, firstMessage, next)
}

func TestOpenAIWSNextAttemptMessageNeverReplaysFirstFrameAfterTurnAdvanced(t *testing.T) {
	next, ok := openAIWSNextAttemptMessage([]byte(`{"type":"response.create","input":"already completed"}`), nil, false, true)
	require.False(t, ok)
	require.Nil(t, next)
}
