package securityaudit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func pageAuditBody(t *testing.T, input ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"input": input})
	require.NoError(t, err)
	return body
}

func pageAuditItem(output string) map[string]any {
	return map[string]any{"type": "function_call_output", "namespace": "agent", "name": "codex_apps_open_page", "output": output}
}

func TestPreparedPromptPageContextIsOwnedAndDoesNotBecomeUserIntent(t *testing.T) {
	cfg, err := ActiveFromStorage(storageConfig{KeywordAllGroups: true,
		CombinationPolicies: []CombinationPolicy{{ID: "garden", Name: "garden", Enabled: true, TopicRules: []string{"orchid"}, IntentRules: []string{"prune"}}},
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, user, output string
		match              bool
	}{
		{"page topic with user intent", "prune it", `{"page_id":"tab-1","text":"orchid"}`, true},
		{"page intent is not user intent", "read the page", `{"page_id":"tab-1","text":"orchid prune"}`, false},
		{"no user intent", "", `{"page_id":"tab-1","text":"orchid prune"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []any{pageAuditItem(tc.output)}
			if tc.user != "" {
				// Context can arrive after the real user message; it must not
				// supersede that message as the latest input.
				items = append([]any{map[string]any{"role": "user", "content": tc.user}}, items...)
			}
			req := preparePromptRequest(Request{Protocol: "responses", Body: pageAuditBody(t, items...)})
			original := string(req.Body)
			scan, err := req.prepared.scanText(req)
			require.NoError(t, err)
			require.Contains(t, scan, tc.output)
			require.Equal(t, original, string(req.Body), "audit must never modify the client wire")
			for i := range req.Body {
				req.Body[i] = '!'
			}
			req.Body = nil
			match, err := cfg.MatchCombination(req)
			require.NoError(t, err)
			require.Equal(t, tc.match, match != nil)
			snapshot, err := req.prepared.snapshot(req, false)
			require.NoError(t, err)
			require.Contains(t, snapshot.FullPrompt, tc.output)
		})
	}
}

func TestPromptPageContextOnlyRecognizesStandaloneDesktopOutputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   bool
	}{
		{"standalone", nil, true},
		{"blank call ID", func(item map[string]any) { item["call_id"] = " " }, true},
		{"tool result", func(item map[string]any) { item["call_id"] = "call_1" }, false},
		{"null call ID", func(item map[string]any) { item["call_id"] = nil }, false},
		{"other namespace", func(item map[string]any) { item["namespace"] = "other" }, false},
		{"unknown name", func(item map[string]any) { item["name"] = "other_page" }, false},
		{"missing page ID", func(item map[string]any) { item["output"] = `{"text":"orchid"}` }, false},
		{"duplicate snapshot key", func(item map[string]any) { item["output"] = `{"page_id":"one","page_id":"two","text":"orchid"}` }, false},
		{"multipart", func(item map[string]any) {
			item["name"] = "codex_apps_selected_page_part_2_of_3"
			item["output"] = "Companion Page snapshot part 2 of 3:\norchid fragment"
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := pageAuditItem(`{"page_id":"one","text":"orchid"}`)
			if tc.change != nil {
				tc.change(item)
			}
			cfg, err := ActiveFromStorage(storageConfig{KeywordAllGroups: true, KeywordBlockingEnabled: true, BlockedKeywords: []string{"orchid"}}, true, prefixEncryptor{})
			require.NoError(t, err)
			service := &PromptService{config: &fakeConfigStore{cfg: cfg, active: true}}
			decision, err := service.CheckKeyword(context.Background(), Request{Protocol: "responses", Body: pageAuditBody(t, item)})
			require.NoError(t, err)
			require.Equal(t, tc.want, decision.Kind == DecisionBlock)
		})
	}
}
