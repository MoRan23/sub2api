package securityaudit

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCombinationMatcherCustomDomain(t *testing.T) {
	// Gardening deliberately shares no topic or intent with either preset. The
	// matcher must obey editable rules rather than recognize built-in domains.
	m, err := newPromptCombinationMatcher([]string{"玫瑰", `regex:(?i)orchids?`}, []string{"修剪", `regex:(?i)prun(?:e|ing)`})
	require.NoError(t, err)
	message := func(role, text string) any { return map[string]any{"role": role, "content": text} }
	for _, tc := range []struct {
		name        string
		body        any
		hit         bool
		topicSource string
	}{
		{"custom current", map[string]any{"input": "修剪玫瑰"}, true, "latest_user"},
		{"custom history", map[string]any{"input": []any{message("user", "玫瑰"), message("assistant", "context"), message("user", "怎样修剪")}}, true, "history"},
		{"intent before topic", map[string]any{"input": "prune the orchid"}, true, "latest_user"},
		{"normalized current", map[string]any{"input": "ＰＲＵＮＥ ＯＲＣＨＩＤＳ"}, true, "latest_user"},
		{"zero-width current", map[string]any{"input": "玫\u200b瑰修\u2060剪"}, true, "latest_user"},
		{"topic alone", map[string]any{"input": "玫瑰开花"}, false, ""},
		{"intent alone", map[string]any{"input": "修剪草坪"}, false, ""},
		{"only history intent", map[string]any{"input": []any{message("user", "修剪玫瑰"), message("user", "今天下雨了吗")}}, false, ""},
		{"preset CBRN does not apply", map[string]any{"input": "make nuclear weapons"}, false, ""},
		{"preset government does not apply", map[string]any{"input": "attack government agency"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, err := m.Match(cbrnTestRequest("responses", tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
			if match != nil {
				require.Equal(t, tc.topicSource, match.Topic.Source)
				require.Equal(t, "latest_user", match.Intent.Source)
			}
		})
	}
	match, err := m.Match(cbrnTestRequest("responses", map[string]any{"input": "玫\u200b瑰修\u2060剪"}))
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "玫\u200b瑰", match.Topic.Content)
	require.Equal(t, "修\u2060剪", match.Intent.Content)
}

func TestCombinationMatcherValidation(t *testing.T) {
	require.NoError(t, validateCombinationRules(nil, nil))
	require.NoError(t, validateCombinationRules(defaultCBRNTopicRules(), defaultCBRNIntentRules()))
	require.NoError(t, validateCombinationRules(defaultGovernmentTopicRules(), defaultGovernmentIntentRules()))
	for _, rule := range []string{"regex:^$", "regex:a*", `regex:\b`, "\u200b"} {
		require.Equal(t, "prompt_audit_combination_empty_match", infraerrors.Reason(validateCombinationRules([]string{rule}, []string{"custom"})))
	}
	require.Equal(t, "prompt_audit_too_many_combination_rules", infraerrors.Reason(validateCombinationRules(make([]string, MaxBlockedKeywords), []string{"extra"})))
	require.Error(t, validateCombinationRules([]string{"regex:(?=x)"}, nil))
	require.Error(t, validateCombinationRules([]string{strings.Repeat("x", MaxBlockedKeywordRunes+1)}, nil))
	// Historical CBRN configuration keeps its validation contract, while policy
	// execution uses the generic constructor above.
	require.Equal(t, "prompt_audit_cbrn_empty_match", infraerrors.Reason(validateCBRNRules([]string{"regex:^$"}, nil)))
}

func TestCombinationMatcherPreparedOwnership(t *testing.T) {
	first, err := newPromptCombinationMatcher([]string{"unmatched"}, []string{"prune"})
	require.NoError(t, err)
	second, err := newPromptCombinationMatcher([]string{"orchid"}, []string{"prune"})
	require.NoError(t, err)
	req := cbrnTestRequest("responses", map[string]any{"input": "ＰＲＵＮＥ ＯＲＣＨＩＤ"})
	prepared, err := preparePromptCombination(req)
	require.NoError(t, err)
	// The prepared view owns extracted strings and must not consult a later
	// reused body buffer, including when another policy has already normalized it.
	for index := range req.Body {
		req.Body[index] = '!'
	}
	require.Nil(t, first.matchPrepared(prepared))
	match := second.matchPrepared(prepared)
	require.NotNil(t, match)
	require.Equal(t, "ＯＲＣＨＩＤ", match.Topic.Content)
	require.Equal(t, "ＰＲＵＮＥ", match.Intent.Content)
	require.Equal(t, 6, match.Topic.Start)
	require.Equal(t, 12, match.Topic.End)
	other, err := preparePromptCombination(cbrnTestRequest("responses", map[string]any{"input": "hello"}))
	require.NoError(t, err)
	require.Nil(t, second.matchPrepared(other))
	require.Equal(t, match, second.matchPrepared(prepared))
}

func TestCombinationMatcherPreparedConcurrentRequests(t *testing.T) {
	miss, err := newPromptCombinationMatcher([]string{"rose"}, []string{"prune"})
	require.NoError(t, err)
	hit, err := newPromptCombinationMatcher([]string{"orchid"}, []string{"prune"})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for index := range 12 {
		wg.Go(func() {
			input := fmt.Sprintf("%d ＰＲＵＮＥ ＯＲＣＨＩＤ", index)
			req := cbrnTestRequest("responses", map[string]any{"input": input})
			for range 4 {
				prepared, err := preparePromptCombination(req)
				if err != nil || miss.matchPrepared(prepared) != nil {
					t.Errorf("invalid first policy result: %v", err)
					return
				}
				match := hit.matchPrepared(prepared)
				if match == nil || match.Topic.Content != "ＯＲＣＨＩＤ" || match.Intent.Content != "ＰＲＵＮＥ" {
					t.Errorf("invalid second policy result: %+v", match)
					return
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkCombinationMatcherPreparedPolicies(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20} {
		for _, count := range []int{1, 8} {
			matchers := make([]*promptCBRNMatcher, count)
			for index := range matchers {
				var err error
				matchers[index], err = newPromptCombinationMatcher([]string{"orchid"}, []string{fmt.Sprintf("intent%d", index)})
				if err != nil {
					b.Fatal(err)
				}
			}
			req := cbrnTestRequest("responses", map[string]any{"input": []any{
				map[string]any{"role": "user", "content": strings.Repeat("x", size)},
				map[string]any{"role": "user", "content": "hello"},
			}})
			for _, shared := range []bool{false, true} {
				b.Run(fmt.Sprintf("bytes=%d/policies=%d/shared=%t", size, count, shared), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if shared {
							prepared, err := preparePromptCombination(req)
							if err != nil {
								b.Fatal(err)
							}
							for _, matcher := range matchers {
								matcher.matchPrepared(prepared)
							}
						} else {
							for _, matcher := range matchers {
								if _, err := matcher.Match(req); err != nil {
									b.Fatal(err)
								}
							}
						}
					}
				})
			}
		}
	}
}

func TestCombinationMatcherBatchIsolation(t *testing.T) {
	m, err := newPromptCombinationMatcher([]string{"orchid"}, []string{"prune"})
	require.NoError(t, err)
	member := func(text string) any {
		return map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}}}
	}
	for _, tc := range []struct {
		name string
		body any
		hit  bool
	}{
		{"first instance matches", map[string]any{"instances": []any{map[string]any{"prompt": "prune orchid"}, map[string]any{"prompt": "hello"}}}, true},
		{"instances cannot combine", map[string]any{"instances": []any{map[string]any{"prompt": "orchid"}, map[string]any{"prompt": "prune grass"}}}, false},
		{"first request matches", map[string]any{"requests": []any{member("prune orchid"), member("hello")}}, true},
		{"requests cannot combine", map[string]any{"requests": []any{member("orchid"), member("prune grass")}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, err := m.Match(cbrnTestRequest("gemini", tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
		})
	}
}
