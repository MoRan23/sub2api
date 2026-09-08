package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	contextBodyClientFirst    = "01989f44-7c00-7000-8000-000000000071"
	contextBodyClientCurrent  = "01989f44-7c00-7000-8000-000000000072"
	contextBodyClientPrevious = "01989f44-7c00-7000-8000-000000000073"
)

func contextBodyBlock(first, current, previous string) string {
	lines := []string{"<context_window>", "Agent name: root"}
	if first != "" {
		lines = append(lines, "First context window id: "+first)
	}
	lines = append(lines, "Current context window id: "+current)
	if previous != "" {
		lines = append(lines, "Previous context window id: "+previous)
	}
	return strings.Join(append(lines, "</context_window>"), "\n")
}

func contextBodyFinalPlan(t *testing.T) OpenAIOAuthIdentityPlan {
	t.Helper()
	plan, err := FinalizeOpenAICodexWirePlan(codexWireProjectionTestPlan(t), string(CodexWireRequestTurn), CodexModelCapabilities{})
	require.NoError(t, err)
	return plan
}

func TestOpenAICodexContextWindowBodyHistoryProjection(t *testing.T) {
	plan := contextBodyFinalPlan(t)
	plan.Window.FirstContextWindowID = codexWireTestContextWindow
	input := contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, contextBodyClientPrevious)
	initial := contextBodyBlock(plan.Window.ContextWindowID, plan.Window.ContextWindowID, "")
	updated, changed := rewriteOpenAICodexContextWindowString(input, plan.Window)
	require.True(t, changed)
	require.Equal(t, initial, updated)

	plan.Window.Number = 2
	plan.Window.LastCompactDigest = strings.Repeat("a", 64)
	plan.Window.ContextWindowID = codexWireTestTurn
	plan.Window.PreviousContextWindowID = codexWireTestParentTurn
	for _, source := range []string{input, contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, "")} {
		updated, changed = rewriteOpenAICodexContextWindowString(source, plan.Window)
		require.True(t, changed)
		require.Equal(t, contextBodyBlock(codexWireTestContextWindow, codexWireTestTurn, codexWireTestParentTurn), updated)
		again, changed := rewriteOpenAICodexContextWindowString(updated, plan.Window)
		require.False(t, changed)
		require.Equal(t, updated, again)
	}
}

func TestOpenAICodexContextWindowBodyLegacyUnknownHistory(t *testing.T) {
	plan := contextBodyFinalPlan(t)
	plan.Window.Number = 8
	plan.Window.LastCompactDigest = strings.Repeat("b", 64)
	plan.Window.FirstContextWindowID = ""
	plan.Window.PreviousContextWindowID = ""
	source := contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, contextBodyClientPrevious)
	updated, changed := rewriteOpenAICodexContextWindowString(source, plan.Window)
	require.True(t, changed)
	require.Equal(t, contextBodyBlock("", plan.Window.ContextWindowID, ""), updated)
	again, changed := rewriteOpenAICodexContextWindowString(updated, plan.Window)
	require.False(t, changed)
	require.Equal(t, updated, again)
}

func TestOpenAICodexContextWindowBodyOnlyRecognizedDeveloperBlocks(t *testing.T) {
	plan := contextBodyFinalPlan(t)
	valid := contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, "")
	for _, source := range []string{
		"Example:\n" + valid,
		strings.Repeat(string(rune(96)), 3) + "\n" + valid,
		valid + "\nordinary instructions",
		strings.TrimSuffix(valid, "</context_window>"),
		strings.Replace(valid, contextBodyClientCurrent, "not-a-uuid", 1),
		strings.Replace(valid, contextBodyClientCurrent, "550e8400-e29b-41d4-a716-446655440000", 1),
		valid + "\n" + valid,
		strings.Replace(valid, "</context_window>", "Current context window id: "+contextBodyClientCurrent+"\n</context_window>", 1),
	} {
		t.Run(source[:min(len(source), 45)], func(t *testing.T) {
			updated, changed := rewriteOpenAICodexContextWindowString(source, plan.Window)
			require.False(t, changed)
			require.Equal(t, source, updated)
		})
	}
	for _, role := range []string{"user", "assistant", "system", "tool"} {
		source, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": role, "content": valid}}})
		require.NoError(t, err)
		updated, err := applyOpenAICodexContextWindowBody(source, plan)
		require.NoError(t, err)
		require.Equal(t, source, updated)
	}
}

func TestOpenAICodexContextWindowBodyContentAndBytePreservation(t *testing.T) {
	plan := contextBodyFinalPlan(t)
	valid := contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, "")
	encoded, err := json.Marshal(valid)
	require.NoError(t, err)
	for _, content := range []string{
		string(encoded),
		"[{\"type\":\"input_text\",\"text\":" + string(encoded) + ",\"unknown\": 1e+07}]",
		"[{\"type\":\"text\",\"text\":" + string(encoded) + "},{\"type\":\"input_image\",\"image_url\":\"untouched\"}]",
	} {
		source := []byte("{ \"model\":\"gpt-5.4\", \"unknown\": 1e+07, \"input\":[{\"role\":\"developer\", \"content\":" + content + "}] }")
		original := append([]byte(nil), source...)
		updated, err := applyOpenAICodexContextWindowBody(source, plan)
		require.NoError(t, err)
		require.Equal(t, original, source)
		require.Contains(t, string(updated), "\"unknown\": 1e+07")
		require.Contains(t, string(updated), "\"model\":\"gpt-5.4\"")
		require.NotContains(t, string(updated), contextBodyClientCurrent)
		require.Contains(t, string(updated), plan.Window.ContextWindowID)
	}
	crlf := " \r\n" + strings.ReplaceAll(valid, "\n", "\r\n") + "\r\n "
	updated, changed := rewriteOpenAICodexContextWindowString(crlf, plan.Window)
	require.True(t, changed)
	require.True(t, strings.HasPrefix(updated, " \r\n<context_window>\r\n"))
	require.True(t, strings.HasSuffix(updated, "</context_window>\r\n "))
	hint := strings.Replace(valid, "</context_window>", "Use history with opaque thread_hint "+contextBodyClientFirst+"\n</context_window>", 1)
	updated, changed = rewriteOpenAICodexContextWindowString(hint, plan.Window)
	require.True(t, changed)
	require.Contains(t, updated, "opaque thread_hint "+contextBodyClientFirst)
}

func TestOpenAICodexContextWindowBodyIdentityGates(t *testing.T) {
	valid := contextBodyBlock(contextBodyClientFirst, contextBodyClientCurrent, "")
	source, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "developer", "content": valid}}})
	require.NoError(t, err)
	for name, change := range map[string]func(*OpenAIOAuthIdentityPlan){
		"disabled":       func(p *OpenAIOAuthIdentityPlan) { p.TurnIdentityRequested = false },
		"not_finalized":  func(p *OpenAIOAuthIdentityPlan) { p.WireProfile.Finalized = false },
		"memory":         func(p *OpenAIOAuthIdentityPlan) { p.WireProfile.RequestKind = CodexWireRequestMemory },
		"wrong_thread":   func(p *OpenAIOAuthIdentityPlan) { p.Window.ThreadID = codexWireTestFork },
		"invalid_window": func(p *OpenAIOAuthIdentityPlan) { p.Window.ContextWindowID = "bad" },
		"headers_only":   func(p *OpenAIOAuthIdentityPlan) { p.ProjectionMode = OpenAIOAuthIdentityProjectionHeadersOnly },
	} {
		t.Run(name, func(t *testing.T) {
			plan := contextBodyFinalPlan(t)
			change(&plan)
			updated, err := applyOpenAICodexContextWindowBody(source, plan)
			require.NoError(t, err)
			require.Equal(t, source, updated)
		})
	}
	for _, mode := range []OpenAIOAuthIdentityProjectionMode{
		OpenAIOAuthIdentityProjectionRegular, OpenAIOAuthIdentityProjectionPassthrough, OpenAIOAuthIdentityProjectionCompact,
	} {
		for _, kind := range []CodexWireRequestKind{CodexWireRequestTurn, CodexWireRequestPrewarm, CodexWireRequestCompaction} {
			plan := contextBodyFinalPlan(t)
			plan.ProjectionMode = mode
			plan.WireProfile.RequestKind = kind
			headers := http.Header{}
			updated, err := ApplyOpenAIOAuthIdentityPlan(headers, source, plan)
			require.NoError(t, err)
			require.Contains(t, gjson.GetBytes(updated, "input.0.content").String(), "Current context window id: "+plan.Window.ContextWindowID)
			require.Equal(t, plan.Window.ContextWindowID, gjson.Get(headers.Get(openAIWSTurnMetadataHeader), "context_window_id").String())
		}
	}
}
