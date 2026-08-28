package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func finalizedCodexCompactionTestProfile(mode CodexCompactionMode) CodexWireProfile {
	implementation := mode.implementation()
	return CodexWireProfile{
		Finalized:      true,
		RequestKind:    CodexWireRequestCompaction,
		CompactionMode: mode,
		Compaction: []byte(`{"trigger":"manual","reason":"user_requested","implementation":"` + implementation +
			`","phase":"standalone_turn","strategy":"memento"}`),
	}
}

func TestOpenAICodexCompactionDeliveryRequiresSuccessfulTerminalAndOneItem(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction","encrypted_content":"x"}}`))
	require.False(t, delivery.Valid())
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"x"}]}}`))
	require.True(t, delivery.Valid(), "the same item in done and completed must be deduplicated")

	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_2","type":"compaction_summary"}}`))
	require.False(t, delivery.Valid())
}

func TestOpenAICodexCompactionDeliveryUsesAddedItemOnlyAsFallback(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.added","item":{"id":"cmp_added","type":"compaction","encrypted_content":"added"}}`))
	require.False(t, delivery.Valid())
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
	require.True(t, delivery.Valid())
}

func TestOpenAICodexCompactionDeliveryAuthoritativeItemOverridesAddedCandidates(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.added","item":{"type":"compaction","status":"in_progress"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.added","item":{"id":"cmp_other","type":"compaction","encrypted_content":"other"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"type":"compaction","status":"completed","encrypted_content":"final"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
	require.True(t, delivery.Valid(), "done items are authoritative over provisional added items")
}

func TestOpenAICodexCompactionDeliveryRejectsMultipleAddedOnlyItems(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.added","item":{"id":"cmp_a","type":"compaction"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.added","item":{"id":"cmp_b","type":"compaction_summary"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
	require.False(t, delivery.Valid())
}

func TestOpenAICodexCompactionDeliveryRejectsFailedTerminal(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.failed","response":{"error":{"message":"failed"}}}`))
	require.False(t, delivery.Valid())
}

func TestOpenAICodexCompactionDeliveryRejectsCompletedEventWithFailedResponseStatus(t *testing.T) {
	delivery := &openAICodexCompactionDelivery{}
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"failed"}}`))
	require.False(t, delivery.Valid())
}

func TestOpenAICodexCompactionDeliveryLocalResponsesRequiresCompletedEvent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		valid   bool
	}{
		{name: "ordinary message", payload: `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message"}]}}`, valid: true},
		{name: "empty output", payload: `{"type":"response.completed","response":{"status":"completed","output":[]}}`, valid: true},
		{name: "done event", payload: `{"type":"response.done","response":{"status":"done","output":[{"type":"message"}]}}`},
		{name: "completed with done status", payload: `{"type":"response.completed","response":{"status":"done","output":[{"type":"message"}]}}`},
		{name: "completed with failed status", payload: `{"type":"response.completed","response":{"status":"failed","output":[{"type":"message"}]}}`},
		{name: "failed event", payload: `{"type":"response.failed","response":{"status":"failed"}}`},
		{name: "done sentinel", payload: `[DONE]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var delivery openAICodexCompactionDelivery
			delivery.ObserveDeliveredEvent([]byte(tc.payload))
			require.Equal(t, tc.valid, delivery.ValidForMode(CodexCompactionModeLocalResponses))
		})
	}

	var eof openAICodexCompactionDelivery
	require.False(t, eof.ValidForMode(CodexCompactionModeLocalResponses), "EOF without response.completed must not commit")
}

func TestOpenAICodexCompactionDeliveryFailureIsStickyForLocalResponses(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.incomplete","response":{"status":"incomplete"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message"}]}}`))
	require.False(t, delivery.ValidForMode(CodexCompactionModeLocalResponses))
}

func TestOpenAICodexCompactionDeliveryRemoteModesRetainExactOneItemContract(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message"}]}}`))
	require.False(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
	require.False(t, delivery.ValidForMode(CodexCompactionModeLegacy))

	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_mode","type":"compaction"}}`))
	require.True(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
	require.True(t, delivery.ValidForMode(CodexCompactionModeLegacy))
}

func TestOpenAICodexCompactionDeliveryRemoteV2RejectsRepeatedDoneBeforeItemDedup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		firstItem  string
		secondItem string
	}{
		{
			name:       "same id",
			firstItem:  `{"id":"cmp_replayed","type":"compaction","encrypted_content":"first"}`,
			secondItem: `{"id":"cmp_replayed","type":"compaction","encrypted_content":"second"}`,
		},
		{
			name:       "same raw",
			firstItem:  `{"type":"compaction_summary","summary":["x"]}`,
			secondItem: `{"type":"compaction_summary","summary":["x"]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var delivery openAICodexCompactionDelivery
			delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":` + tc.firstItem + `}`))
			delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":` + tc.secondItem + `}`))
			delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[` + tc.firstItem + `]}}`))

			require.False(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
			require.True(t, delivery.ValidForMode(CodexCompactionModeLegacy), "legacy retains identity-based item deduplication")
			require.True(t, delivery.ValidForMode(CodexCompactionModeLocalResponses), "local mode ignores remote-v2 done cardinality")
		})
	}
}

func TestOpenAICodexCompactionDeliveryRemoteV2AcceptsOneDoneAndMatchingTerminalItem(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_once","type":"compaction","encrypted_content":"x"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_once","type":"compaction","encrypted_content":"x"}]}}`))

	require.True(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
}

func TestOpenAICodexCompactionDeliveryRemoteV2AcceptsOneDoneAndEmptyTerminalOutput(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_once","type":"compaction","encrypted_content":"x"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))

	require.True(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
}

func TestOpenAICodexCompactionDeliveryRemoteV2IgnoresTerminalCompactionIdentity(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_done","type":"compaction","encrypted_content":"done"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_terminal","type":"compaction","encrypted_content":"terminal"}]}}`))

	require.True(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
}

func TestOpenAICodexCompactionDeliveryRemoteV2FailureRemainsSticky(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_once","type":"compaction","encrypted_content":"x"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.failed","response":{"status":"failed"}}`))
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))

	require.False(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
}

func TestOpenAICodexCompactionDeliveryRemoteV2RejectsFallbackOnlyEvidence(t *testing.T) {
	tests := []struct {
		name   string
		events []string
	}{
		{
			name: "added only",
			events: []string{
				`{"type":"response.output_item.added","item":{"id":"cmp_added","type":"compaction","encrypted_content":"x"}}`,
				`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
			},
		},
		{
			name: "terminal output only",
			events: []string{
				`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_terminal","type":"compaction","encrypted_content":"x"}]}}`,
			},
		},
		{
			name: "response done",
			events: []string{
				`{"type":"response.output_item.done","item":{"id":"cmp_done","type":"compaction","encrypted_content":"x"}}`,
				`{"type":"response.done","response":{"status":"done","output":[{"id":"cmp_done","type":"compaction","encrypted_content":"x"}]}}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var delivery openAICodexCompactionDelivery
			for _, event := range tt.events {
				delivery.ObserveDeliveredEvent([]byte(event))
			}
			require.False(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
		})
	}
}

func TestOpenAICodexCompactionDeliveryLegacyRetainsFallbackContracts(t *testing.T) {
	tests := []struct {
		name   string
		events []string
	}{
		{
			name: "added only",
			events: []string{
				`{"type":"response.output_item.added","item":{"id":"cmp_added","type":"compaction","encrypted_content":"x"}}`,
				`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
			},
		},
		{
			name: "terminal output only",
			events: []string{
				`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_terminal","type":"compaction","encrypted_content":"x"}]}}`,
			},
		},
		{
			name: "response done",
			events: []string{
				`{"type":"response.done","response":{"status":"done","output":[{"id":"cmp_done","type":"compaction","encrypted_content":"x"}]}}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var delivery openAICodexCompactionDelivery
			for _, event := range tt.events {
				delivery.ObserveDeliveredEvent([]byte(event))
			}
			require.True(t, delivery.ValidForMode(CodexCompactionModeLegacy))
		})
	}
}

func TestOpenAICodexJSONCompactionOutputValid(t *testing.T) {
	require.True(t, openAICodexJSONCompactionOutputValid([]byte(`{"output":[{"type":"message"},{"id":"cmp_1","type":"compaction"}]}`)))
	require.True(t, openAICodexJSONCompactionOutputValid([]byte(`{"output":[{"id":"cmp_1","type":"compaction"},{"id":"cmp_1","type":"compaction"}]}`)), "pure JSON retains identity-based item deduplication")
	require.False(t, openAICodexJSONCompactionOutputValid([]byte(`{"output":[]}`)))
	require.False(t, openAICodexJSONCompactionOutputValid([]byte(`{"output":[{"id":"a","type":"compaction"},{"id":"b","type":"compaction_summary"}]}`)))
}

func TestOpenAIJSONCompactionDeliveryCannotProveRemoteV2DoneEvent(t *testing.T) {
	delivery := openAIJSONCompactionDelivery([]byte(`{"status":"completed","output":[{"id":"cmp_json","type":"compaction","encrypted_content":"x"}]}`))

	require.False(t, delivery.ValidForMode(CodexCompactionModeRemoteV2))
	require.True(t, delivery.ValidForMode(CodexCompactionModeLegacy))
}
