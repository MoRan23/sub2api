package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func prepareTimezoneReplayTestState(t *testing.T, body []byte, acceptedAt time.Time, target RequestLocationObservation) ([]byte, *RequestTimezoneState) {
	t.Helper()
	_, state := PrepareOpenAIRequestTimezone(body, timezoneTestPolicy(), acceptedAt, false, true)
	state = state.WithTarget(target)
	projected, ok := state.ApplyToBody(body)
	require.True(t, ok)
	return projected, state
}

func timezoneReplayTestTokyo() RequestLocationObservation {
	return RequestLocationObservation{Type: "approximate", Country: "JP", Region: "Tokyo", City: "Tokyo", Timezone: "Asia/Tokyo"}
}

func TestOpenAIWSTimezoneReplayTracksMovedSourcesWithoutAuthorizingCopies(t *testing.T) {
	message := timezoneTestMessage(timezoneWSEnvironment)
	message["model"], message["session"] = "client-model", "client-session"
	body := timezoneTestBody(t, map[string]any{
		"type": "response.create", "model": "gpt-5.1",
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "tool_1", "output": "done"},
			message,
		},
	})
	projected, state := prepareTimezoneReplayTestState(t, body, time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC), openAIRequestSearchLocation())
	var err error
	projected, err = sjson.SetBytes(projected, "input.1.model", "adapted-model")
	require.NoError(t, err)
	projected, err = sjson.SetBytes(projected, "input.1.session", "adapted-session")
	require.NoError(t, err)
	projected, err = sjson.SetRawBytes(projected, "input.1.extension", []byte(`{"exact":9007199254740993}`))
	require.NoError(t, err)

	ledger := newOpenAIWSTimezoneReplayLedger()
	items, exists, err := ledger.Record(projected, state)
	require.NoError(t, err)
	require.True(t, exists)
	require.Len(t, items, 2)
	require.Equal(t, timezoneWSEnvironment, gjson.GetBytes(items[1], "content.0.text").String())
	require.Equal(t, "adapted-model", gjson.GetBytes(items[1], "model").String())
	require.Equal(t, "adapted-session", gjson.GetBytes(items[1], "session").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(items[1], "extension.exact").Raw)

	// Appending history and removing its tool item moves the original source
	// from input.1 to input.2. An independently copied item has no ledger entry.
	copiedReference := append(json.RawMessage(nil), items[1]...)
	replay := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":"earlier history"}`),
		json.RawMessage(`{"role":"assistant","content":"later history"}`),
		items[1], copiedReference,
	}
	result, aggregate, ok := ledger.ProjectReplay(projected, replay, timezoneReplayTestTokyo(), state)
	require.True(t, ok)
	require.Len(t, result, 4)
	require.Contains(t, gjson.GetBytes(result[2], "content.0.text").String(), "<timezone>Asia/Tokyo</timezone>")
	require.JSONEq(t, string(copiedReference), string(result[3]), "equal source text cannot authorize a newly allocated reference")
	require.Equal(t, "adapted-model", gjson.GetBytes(result[2], "model").String())
	require.Equal(t, "adapted-session", gjson.GetBytes(result[2], "session").String())
	require.Len(t, aggregate.Conversions, 1)
	require.Equal(t, "input.2.content.0.text", aggregate.Conversions[0].Path)
	require.Equal(t, timezoneWSEnvironment, gjson.GetBytes(items[1], "content.0.text").String(), "projection must leave neutral replay storage unchanged")
}

func TestOpenAIWSTimezoneReplayCommitFreezesSuccessfulHistoricalDate(t *testing.T) {
	ledger := newOpenAIWSTimezoneReplayLedger()
	body := timezoneWSBody(timezoneWSEnvironment)
	firstBody, firstState := prepareTimezoneReplayTestState(t, body, time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC), openAIRequestSearchLocation())
	history, exists, err := ledger.Record(firstBody, firstState)
	require.NoError(t, err)
	require.True(t, exists)
	first, _, ok := ledger.ProjectReplay(firstBody, history, openAIRequestSearchLocation(), firstState)
	require.True(t, ok)
	require.Contains(t, gjson.GetBytes(first[0], "content.0.text").String(), "<current_date>2026-01-01</current_date>")
	ledger.Commit(first)
	ledger.Commit(history) // Both bridge histories may commit the same source.

	currentBody, currentState := prepareTimezoneReplayTestState(t, body, time.Date(2026, 1, 3, 0, 1, 0, 0, time.UTC), openAIRequestSearchLocation())
	current, exists, err := ledger.Record(currentBody, currentState)
	require.NoError(t, err)
	require.True(t, exists)
	replay := append(append([]json.RawMessage(nil), history...), current...)
	result, aggregate, ok := ledger.ProjectReplay(currentBody, replay, timezoneReplayTestTokyo(), currentState)
	require.True(t, ok)
	require.Len(t, result, 2)
	historicalText := gjson.GetBytes(result[0], "content.0.text").String()
	currentText := gjson.GetBytes(result[1], "content.0.text").String()
	require.Contains(t, historicalText, "<timezone>Asia/Tokyo</timezone>")
	require.Contains(t, historicalText, "<current_date>2026-01-01</current_date>", "history retains the date first successfully sent, not its original or newly targeted date")
	require.Contains(t, currentText, "<timezone>Asia/Tokyo</timezone>")
	require.Contains(t, currentText, "<current_date>2026-01-03</current_date>")
	require.Equal(t, currentState.AcceptedAt, aggregate.AcceptedAt)
}

func TestOpenAIWSTimezoneReplayNilStatePassesThrough(t *testing.T) {
	ledger := newOpenAIWSTimezoneReplayLedger()
	body := timezoneWSBody(timezoneWSEnvironment)
	items, exists, err := ledger.Record(body, nil)
	require.NoError(t, err)
	require.True(t, exists)
	require.Len(t, items, 1)
	require.Equal(t, timezoneWSEnvironment, gjson.GetBytes(items[0], "content.0.text").String())
	result, aggregate, ok := ledger.ProjectReplay(body, items, timezoneReplayTestTokyo(), nil)
	require.True(t, ok)
	require.Nil(t, aggregate)
	require.Equal(t, items, result)
	ledger.Commit(items)
}

func TestOpenAIWSTimezoneReplayDoesNotAuthorizePreviouslyLimitedSource(t *testing.T) {
	ledger := newOpenAIWSTimezoneReplayLedger()
	acceptedAt := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	limitedBody := timezoneTestBody(t, map[string]any{"type": "response.create", "input": []any{
		timezoneTestMessage(timezoneWSEnvironment),
		map[string]any{"role": "user", "content": strings.Repeat("x", openAIRequestTimezoneTextLimit)},
	}})
	limitedBody, limitedState := prepareTimezoneReplayTestState(t, limitedBody, acceptedAt, openAIRequestSearchLocation())
	require.Equal(t, "limited", limitedState.Inbound.ScanStatus)
	require.NotEmpty(t, limitedState.Inbound.Items, "the source must have been observed before the later scan limit")
	history, exists, err := ledger.Record(limitedBody, limitedState)
	require.NoError(t, err)
	require.True(t, exists)
	ledger.Commit(history)

	currentBody, currentState := prepareTimezoneReplayTestState(t, timezoneWSBody(timezoneWSEnvironment), acceptedAt.Add(24*time.Hour), openAIRequestSearchLocation())
	current, exists, err := ledger.Record(currentBody, currentState)
	require.NoError(t, err)
	require.True(t, exists)
	// Trimming the large item makes this replay small enough to scan completely;
	// the earlier limit must still prevent authorizing its retained source.
	replay := append([]json.RawMessage{history[0]}, current...)
	result, _, ok := ledger.ProjectReplay(currentBody, replay, timezoneReplayTestTokyo(), currentState)
	require.True(t, ok)
	require.Equal(t, timezoneWSEnvironment, gjson.GetBytes(result[0], "content.0.text").String())
	require.Contains(t, gjson.GetBytes(result[1], "content.0.text").String(), "<timezone>Asia/Tokyo</timezone>")
}

func TestOpenAIWSTimezoneReplayPreservesCurrentSearchLocationSource(t *testing.T) {
	ledger := newOpenAIWSTimezoneReplayLedger()
	acceptedAt := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	historyBody, historyState := prepareTimezoneReplayTestState(t, timezoneWSBody(timezoneWSEnvironment), acceptedAt, openAIRequestSearchLocation())
	history, exists, err := ledger.Record(historyBody, historyState)
	require.NoError(t, err)
	require.True(t, exists)
	ledger.Commit(history)

	body := timezoneTestBody(t, map[string]any{
		"type": "response.create", "model": "current-model", "input": timezoneTestInput(timezoneWSEnvironment),
		"tools": []any{map[string]any{
			"type": "web_search", "search_context_size": "high", "extension": json.RawMessage(`{"exact":9007199254740993}`),
			"user_location": map[string]any{"type": "approximate", "country": "FR", "region": "IDF", "city": "Paris", "timezone": "Europe/Paris"},
		}},
	})
	currentBody, currentState := prepareTimezoneReplayTestState(t, body, acceptedAt.Add(24*time.Hour), openAIRequestSearchLocation())
	withoutTools, err := sjson.DeleteBytes(currentBody, "tools")
	require.NoError(t, err)
	withoutToolItems, exists, err := newOpenAIWSTimezoneReplayLedger().Record(withoutTools, currentState)
	require.NoError(t, err, "an adapter removing non-input tools must not prevent recording input sources")
	require.True(t, exists)
	require.Equal(t, timezoneWSEnvironment, gjson.GetBytes(withoutToolItems[0], "content.0.text").String())
	current, exists, err := ledger.Record(currentBody, currentState)
	require.NoError(t, err)
	require.True(t, exists)
	replay := append(append([]json.RawMessage(nil), history...), current...)
	projectedItems, aggregate, ok := ledger.ProjectReplay(currentBody, replay, timezoneReplayTestTokyo(), currentState)
	require.True(t, ok)
	// Apply the aggregate to the neutral current request, retaining its tool
	// carrier while replacing only input with the ledger's projected replay.
	finalBody, err := setOpenAIWSPayloadInputSequence(body, projectedItems, true)
	require.NoError(t, err)
	finalBody, ok = aggregate.ApplyToBody(finalBody)
	require.True(t, ok)
	require.JSONEq(t, string(finalBody), string(aggregate.PreparedBody()), "the aggregate snapshot must include expanded replay and the current tool projection")
	require.Equal(t, "current-model", gjson.GetBytes(finalBody, "model").String())
	require.Equal(t, "web_search", gjson.GetBytes(finalBody, "tools.0.type").String())
	require.Equal(t, "high", gjson.GetBytes(finalBody, "tools.0.search_context_size").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(finalBody, "tools.0.extension.exact").Raw)
	require.JSONEq(t, string(timezoneTestBody(t, timezoneReplayTestTokyo())), gjson.GetBytes(finalBody, "tools.0.user_location").Raw)
	require.Len(t, aggregate.Conversions, 3, "the two replay environments must not replace the current non-input source")
}

func TestOpenAIWSTimezoneReplayTrimReplacesFullHistoryIdentities(t *testing.T) {
	ledger := newOpenAIWSTimezoneReplayLedger()
	acceptedAt := time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC)
	var messages []any
	var retained []json.RawMessage
	var retired []openAIWSTimezoneReplayItemID
	var currentBody []byte
	var currentState *RequestTimezoneState
	for turn := 1; turn <= 5; turn++ {
		for _, item := range retained {
			retired = append(retired, openAIWSTimezoneReplayIdentity(item))
		}
		messages = append(messages, timezoneTestMessage(timezoneWSEnvironment))
		body := timezoneTestBody(t, map[string]any{"type": "response.create", "input": messages})
		currentBody, currentState = prepareTimezoneReplayTestState(t, body, acceptedAt.Add(time.Duration(turn)*time.Hour), openAIRequestSearchLocation())
		var exists bool
		var err error
		retained, exists, err = ledger.Record(currentBody, currentState)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, retained, turn)
		if turn > 1 {
			require.Greater(t, len(ledger.items), turn, "re-recording a full history creates new identities before the old cache is retired")
		}
		projected, _, ok := ledger.ProjectReplay(currentBody, retained, openAIRequestSearchLocation(), currentState)
		require.True(t, ok)
		require.Len(t, ledger.projectedItems, turn)
		ledger.Commit(projected)
		ledger.Trim(retained)
		require.Len(t, ledger.items, turn, "only the latest full-history cache may retain source records")
		require.Empty(t, ledger.projectedItems, "a retained neutral source must not implicitly retain its projected aliases")
		for _, identity := range retired {
			require.Nil(t, ledger.items[identity])
			require.Nil(t, ledger.projectedItems[identity])
		}
		for _, item := range retained {
			require.NotNil(t, ledger.items[openAIWSTimezoneReplayIdentity(item)])
		}
	}

	projected, aggregate, ok := ledger.ProjectReplay(currentBody, retained, timezoneReplayTestTokyo(), currentState)
	require.True(t, ok)
	require.Len(t, aggregate.Conversions, len(retained))
	for _, item := range projected {
		require.Contains(t, gjson.GetBytes(item, "content.0.text").String(), "<timezone>Asia/Tokyo</timezone>")
	}
	ledger.Trim()
	require.Empty(t, ledger.items)
	require.Empty(t, ledger.projectedItems)
}

func TestOpenAIWSTimezoneReplayTrimPreservesCacheUnionWithoutAuthorizingCopies(t *testing.T) {
	ledger := newOpenAIWSTimezoneReplayLedger()
	body, state := prepareTimezoneReplayTestState(t, timezoneWSBody(timezoneWSEnvironment), time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC), openAIRequestSearchLocation())
	var sources []json.RawMessage
	for range 3 {
		items, exists, err := ledger.Record(body, state)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		sources = append(sources, items[0])
	}
	projected, _, ok := ledger.ProjectReplay(body, sources, openAIRequestSearchLocation(), state)
	require.True(t, ok)
	copiedReference := append(json.RawMessage(nil), sources[0]...)
	firstID := openAIWSTimezoneReplayIdentity(sources[0])
	secondID := openAIWSTimezoneReplayIdentity(sources[1])
	droppedID := openAIWSTimezoneReplayIdentity(sources[2])
	aliasID := openAIWSTimezoneReplayIdentity(projected[0])
	copyID := openAIWSTimezoneReplayIdentity(copiedReference)

	ledger.Trim(
		[]json.RawMessage{sources[0], projected[0]},
		[]json.RawMessage{sources[0], sources[1], copiedReference},
	)
	require.Len(t, ledger.items, 2, "the two histories retain their identity union, not duplicate records")
	require.NotNil(t, ledger.items[firstID])
	require.NotNil(t, ledger.items[secondID])
	require.Nil(t, ledger.items[droppedID])
	require.Nil(t, ledger.items[copyID], "retaining equal bytes does not create source authority")
	require.Len(t, ledger.projectedItems, 1)
	require.NotNil(t, ledger.projectedItems[aliasID])
	require.Nil(t, ledger.projectedItems[openAIWSTimezoneReplayIdentity(projected[1])])
	ledger.Commit([]json.RawMessage{projected[0]})
	require.True(t, ledger.items[firstID].historical, "an explicitly retained alias still supports Commit")
	require.False(t, ledger.items[secondID].historical)

	replay := []json.RawMessage{sources[1], copiedReference, sources[2], sources[0]}
	result, aggregate, ok := ledger.ProjectReplay(body, replay, timezoneReplayTestTokyo(), state)
	require.True(t, ok)
	require.Len(t, aggregate.Conversions, 2)
	require.Contains(t, gjson.GetBytes(result[0], "content.0.text").String(), "<timezone>Asia/Tokyo</timezone>")
	require.Contains(t, gjson.GetBytes(result[0], "content.0.text").String(), "<current_date>2026-01-02</current_date>")
	require.Equal(t, timezoneWSEnvironment, gjson.GetBytes(result[1], "content.0.text").String())
	require.Equal(t, timezoneWSEnvironment, gjson.GetBytes(result[2], "content.0.text").String(), "a retired source identity has no remaining replay authority")
	require.Contains(t, gjson.GetBytes(result[3], "content.0.text").String(), "<timezone>Asia/Tokyo</timezone>")
	require.Contains(t, gjson.GetBytes(result[3], "content.0.text").String(), "<current_date>2026-01-01</current_date>")

	ledger.Trim([]json.RawMessage{result[0]})
	require.Empty(t, ledger.items, "retaining a projected alias cannot promote it to a neutral source")
	require.Len(t, ledger.projectedItems, 1)
	unchanged, untracked, ok := ledger.ProjectReplay(body, []json.RawMessage{result[0]}, openAIRequestSearchLocation(), state)
	require.True(t, ok)
	require.Empty(t, untracked.Conversions)
	require.JSONEq(t, string(result[0]), string(unchanged[0]))
}
