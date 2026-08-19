package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICodexCompactionDeliveryRequiresSuccessfulTerminalAndOneItem(t *testing.T) {
	var delivery openAICodexCompactionDelivery
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction","encrypted_content":"x"}}`))
	require.False(t, delivery.Valid())
	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.completed","response":{"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"x"}]}}`))
	require.True(t, delivery.Valid(), "the same item in done and completed must be deduplicated")

	delivery.ObserveDeliveredEvent([]byte(`{"type":"response.output_item.done","item":{"id":"cmp_2","type":"compaction_summary"}}`))
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

func TestOpenAICodexJSONCompactionOutputValid(t *testing.T) {
	require.True(t, openAICodexJSONCompactionOutputValid([]byte(`{"output":[{"type":"message"},{"id":"cmp_1","type":"compaction"}]}`)))
	require.False(t, openAICodexJSONCompactionOutputValid([]byte(`{"output":[]}`)))
	require.False(t, openAICodexJSONCompactionOutputValid([]byte(`{"output":[{"id":"a","type":"compaction"},{"id":"b","type":"compaction_summary"}]}`)))
}
