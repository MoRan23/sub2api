package securityaudit

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func cbrnTestRequest(protocol string, body any) Request {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return Request{Protocol: protocol, Body: raw}
}

func TestCBRNPresetRules(t *testing.T) {
	m, err := newPromptCBRNMatcher(defaultCBRNTopicRules(), defaultCBRNIntentRules())
	require.NoError(t, err)
	for _, tc := range []struct {
		text string
		hit  bool
	}{
		{"核武器制造", true}, {"核武器歷史", false}, {"化學武器製造", true},
		{"沙林的合成", true}, {"怎样防止化学武器制造", true}, {"科研教学讨论炭疽杆菌培养", true},
		{"HOW TO MAKE A NUCLEAR WEAPON", true}, {"ＢＵＹ ＳＡＲＩＮ", true},
		{"sarin manufacture", true}, {"manufacture sarin", true}, {"buy nerve agents", true},
		{"沙\u200b林的制\u2060造", true}, {"请介绍化学实验步骤", false},
		{"怎么培养细菌", false}, {"购买VX商品", false}, {"研究病毒的历史", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			match, err := m.Match(cbrnTestRequest("responses", map[string]any{"input": tc.text}))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
		})
	}
}

func TestCBRNLatestUserBoundary(t *testing.T) {
	m, err := newPromptCBRNMatcher([]string{"nuclear", "沙林"}, []string{"make", "步骤", "制造"})
	require.NoError(t, err)
	message := func(role string, text any) any { return map[string]any{"role": role, "content": text} }
	for _, tc := range []struct {
		name, protocol string
		body           any
		hit            bool
		source         string
	}{
		{"current", "chat_completions", map[string]any{"messages": []any{message("user", "make nuclear")}}, true, "latest_user"},
		{"carried user history", "chat_completions", map[string]any{"messages": []any{message("user", "nuclear"), message("assistant", "context"), message("user", "make")}}, true, "history"},
		{"carried assistant history", "messages", map[string]any{"messages": []any{message("assistant", "nuclear"), message("user", "make")}}, true, "history"},
		{"history intent excluded", "chat_completions", map[string]any{"messages": []any{message("user", "make nuclear"), message("assistant", "context"), message("user", "hello")}}, false, ""},
		{"adjacent distinct users", "chat_completions", map[string]any{"messages": []any{message("user", "make nuclear"), message("user", "hello")}}, false, ""},
		{"latest image only", "chat_completions", map[string]any{"messages": []any{message("user", "make nuclear"), message("user", []any{map[string]any{"type": "image_url", "image_url": "fixture"}})}}, false, ""},
		{"latest empty", "messages", map[string]any{"messages": []any{message("user", "make nuclear"), message("user", "")}}, false, ""},
		{"tool only", "chat_completions", map[string]any{"messages": []any{message("tool", "make nuclear")}}, false, ""},
		{"system only", "responses", map[string]any{"instructions": "make nuclear"}, false, ""},
		{"function output", "responses", map[string]any{"input": []any{map[string]any{"type": "function_call_output", "call_id": "a", "text": "make nuclear", "output": "make nuclear"}}}, false, ""},
		{"previous response only", "responses", map[string]any{"previous_response_id": "resp_old", "input": "make"}, false, ""},
		{"multi content blocks", "responses", map[string]any{"input": []any{message("user", []any{map[string]any{"type": "input_text", "text": "nuclear"}, map[string]any{"type": "input_text", "text": "make"}})}}, true, "latest_user"},
		{"responses current image only", "responses", map[string]any{"input": []any{message("user", "make nuclear"), message("user", []any{map[string]any{"type": "input_image", "image_url": "fixture"}})}}, false, ""},
		{"ws create", "responses_websocket", map[string]any{"type": "response.create", "input": "make nuclear"}, true, "latest_user"},
		{"ws nested", "responses_websocket", map[string]any{"type": "response.create", "response": map[string]any{"input": "make nuclear"}}, true, "latest_user"},
		{"ws control", "responses_websocket", map[string]any{"type": "response.interrupt", "input": "make nuclear"}, false, ""},
		{"media", "images", map[string]any{"prompt": "make nuclear"}, true, "latest_user"},
		{"gemini", "gemini", map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "nuclear"}, map[string]any{"text": "make"}}}}}, true, "latest_user"},
		{"gemini history intent", "gemini", map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "make nuclear"}}}, map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}}}, false, ""},
		{"gemini latest image", "gemini", map[string]any{"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "make nuclear"}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"inlineData": map[string]any{"data": "fixture"}}}},
		}}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, err := m.Match(cbrnTestRequest(tc.protocol, tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
			if tc.hit {
				require.Equal(t, tc.source, match.Topic.Source)
				require.Equal(t, "latest_user", match.Intent.Source)
			}
		})
	}
}

func TestCBRNOriginalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, original, rule, want string
		start, end                 int
	}{
		{"full width", "x ＳＡＲＩＮ make", "regex:(?i)sarin", "ＳＡＲＩＮ", 2, 7},
		{"ligature expansion", "x ﬃ make", "regex:ffi", "ﬃ", 2, 3},
		{"part ligature", "x ﬃ make", "regex:fi", "ﬃ", 2, 3},
		{"composition", "x e\u0301 make", "regex:é", "e\u0301", 2, 4},
		{"zero width internal", "x sa\u200brin make", "regex:sarin", "sa\u200brin", 2, 8},
		{"all stripped", "\ufeffx\u200b sa\u200cr\u200di\u2060n make", "regex:sarin", "sa\u200cr\u200di\u2060n", 4, 12},
		{"case literal", "x SaRiN make", "sarin", "SaRiN", 2, 7},
		{"normalized literal", "x sarin make", "ＳＡＲＩＮ", "sarin", 2, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := newPromptCBRNMatcher([]string{tc.rule}, []string{"make"})
			require.NoError(t, err)
			req := cbrnTestRequest("responses", map[string]any{"input": tc.original})
			before := string(req.Body)
			match, err := m.Match(req)
			require.NoError(t, err)
			require.NotNil(t, match)
			require.Equal(t, tc.want, match.Topic.Content)
			require.Equal(t, tc.start, match.Topic.Start)
			require.Equal(t, tc.end, match.Topic.End)
			require.Equal(t, before, string(req.Body))
			require.Equal(t, tc.want, string([]rune(tc.original)[match.Topic.Start:match.Topic.End]))
		})
	}
}

func TestCBRNGeminiBatchIsolation(t *testing.T) {
	m, err := newPromptCBRNMatcher([]string{"nuclear"}, []string{"make"})
	require.NoError(t, err)
	user := func(text string) map[string]any {
		return map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}}}
	}
	for _, tc := range []struct {
		name        string
		body        map[string]any
		hit         bool
		topicIndex  int
		intentIndex int
	}{
		{"first instance", map[string]any{"instances": []any{map[string]any{"prompt": "make nuclear"}, map[string]any{"prompt": "hello"}}}, true, 0, 0},
		{"last instance", map[string]any{"instances": []any{map[string]any{"prompt": "hello"}, map[string]any{"prompt": "make nuclear"}}}, true, 1, 1},
		{"no cross instance conditions", map[string]any{"instances": []any{map[string]any{"prompt": "nuclear"}, map[string]any{"prompt": "make paper"}}}, false, 0, 0},
		{"first request", map[string]any{"requests": []any{user("make nuclear"), user("hello")}}, true, 0, 0},
		{"last request", map[string]any{"requests": []any{user("hello"), user("make nuclear")}}, true, 1, 1},
		{"no cross request conditions", map[string]any{"requests": []any{user("nuclear"), user("make paper")}}, false, 0, 0},
		{"request instances independent", map[string]any{"requests": []any{map[string]any{"instances": []any{map[string]any{"prompt": "nuclear"}, map[string]any{"prompt": "make paper"}}}, user("hello")}}, false, 0, 0},
		{"request instance offsets", map[string]any{"systemInstruction": "system", "instances": []any{map[string]any{"prompt": "hello"}}, "requests": []any{user("hello"), map[string]any{"systemInstruction": "second system", "instances": []any{map[string]any{"prompt": "hello"}, map[string]any{"prompt": "make nuclear"}}}}}, true, 5, 5},
		{"shared instance system", map[string]any{"systemInstruction": "nuclear", "instances": []any{map[string]any{"prompt": "hello"}, map[string]any{"prompt": "make"}}}, true, 0, 2},
		{"no root cross request", map[string]any{"instances": []any{map[string]any{"prompt": "nuclear"}}, "requests": []any{user("make paper")}}, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := cbrnTestRequest("gemini", tc.body)
			match, err := m.Match(req)
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
			if match != nil {
				require.Equal(t, tc.topicIndex, match.Topic.SegmentIndex)
				require.Equal(t, tc.intentIndex, match.Intent.SegmentIndex)
				var document any
				require.NoError(t, json.Unmarshal(req.Body, &document))
				segments := extractProtocolSegments(req.Protocol, document)
				for _, evidence := range []CBRNMatchEvidence{match.Topic, match.Intent} {
					runes := []rune(segments[evidence.SegmentIndex].text)
					require.Equal(t, evidence.Content, string(runes[evidence.Start:evidence.End]))
				}
			}
		})
	}
}

func TestCBRNRulePriorityAndValidation(t *testing.T) {
	m, err := newPromptCBRNMatcher([]string{"regex:second", "first"}, []string{"make"})
	require.NoError(t, err)
	match, err := m.Match(cbrnTestRequest("responses", map[string]any{"input": "first second make"}))
	require.NoError(t, err)
	require.Equal(t, "regex:second", match.Topic.Rule)
	for _, rule := range []string{"regex:^$", "regex:a*", "regex:(?i)(?:)", `regex:\b`, `regex:(?:\b|word)`, "\u200b"} {
		require.Equal(t, "prompt_audit_cbrn_empty_match", infraerrors.Reason(validateCBRNRules([]string{rule}, []string{"make"})))
	}
	require.Error(t, validateCBRNRules([]string{"regex:(?=x)"}, nil))
	require.Error(t, validateCBRNRules([]string{strings.Repeat("x", MaxBlockedKeywordRunes+1)}, nil))
	require.Error(t, validateCBRNRules([]string{"regex:" + strings.Repeat("x", MaxBlockedRegexRunes)}, nil))
	require.Error(t, validateCBRNRules(make([]string, MaxBlockedKeywords), []string{"extra"}))
	require.NoError(t, validateCBRNRules(nil, nil))
	_, err = m.Match(Request{Protocol: "responses", Body: []byte(`{"invalid`)})
	require.Error(t, err)
}

func TestCBRNConcurrentAndBoundedEvidence(t *testing.T) {
	m, err := newPromptCBRNMatcher([]string{"regex:x+"}, []string{"make"})
	require.NoError(t, err)
	req := cbrnTestRequest("responses", map[string]any{"input": strings.Repeat("x", 65536) + " make"})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 4 {
				match, err := m.Match(req)
				if err != nil || match == nil || utf8.RuneCountInString(match.Topic.Content) > MaxBlockedRegexRunes+1 || match.Topic.End != 65536 {
					t.Errorf("invalid bounded match: %v %v", match, err)
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkCBRNMatcherLargeRequest(b *testing.B) {
	m, err := newPromptCBRNMatcher(defaultCBRNTopicRules(), defaultCBRNIntentRules())
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{64 << 10, 1 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			req := cbrnTestRequest("responses", map[string]any{"input": strings.Repeat("x", size) + " make nuclear weapons"})
			b.ReportAllocs()
			b.SetBytes(int64(len(req.Body)))
			b.ResetTimer()
			for b.Loop() {
				if _, err := m.Match(req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
