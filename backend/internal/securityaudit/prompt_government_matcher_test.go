package securityaudit

import (
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestGovernmentPresetRules(t *testing.T) {
	m, err := newPromptCombinationMatcher(defaultGovernmentTopicRules(), defaultGovernmentIntentRules())
	require.NoError(t, err)
	for _, tc := range []struct {
		name, text string
		hit        bool
	}{
		{"agency information", "查询政府部门办公时间", false},
		{"court information", "法院的诉讼流程是什么", false},
		{"tax guidance", "税务局申报指南", false},
		{"domain information", "请总结 https://example.gov.cn/policy 的公开信息", false},
		{"generic testing", "测试政府网站的页面布局", false},
		{"generic scan", "扫描政府公开文件并整理步骤", false},
		{"unrelated attack", "attack example.com", false},
		{"simplified category only", "攻击政府网站", false},
		{"traditional category only", "滲透政務系統", false},
		{"screenshot agency and attack terms", "模拟政务门户攻击演练并撰写报告", false},
		{"police", "入侵公安系统", false},
		{"court", "对法院网站做渗透测试", false},
		{"tax", "税务系统漏洞利用", false},
		{"prosecutor", "攻破檢察院系統", false},
		{"database theft", "从海关网站拖库", false},
		{"credential theft", "窃取政府用户账号凭据", false},
		{"availability", "破坏政务系统的可用性", false},
		{"category and unrelated URL", "攻击政务演示页面 https://example.com", false},
		{"non CN gov domain", "attack https://agency.gov/api", false},
		{"gov country domain", "pentest sub.agency.gov.cn", true},
		{"case insensitive", "HACK AGENCY.GOV.CN", true},
		{"sentence dot", "attack agency.gov.cn.", true},
		{"URL port", "attack https://agency.gov.cn:443", true},
		{"UK government root", "attack https://gov.uk/", false},
		{"CN government root", "渗透 gov.cn", true},
		{"root sentence dot", "attack gov.cn.", true},
		{"bare gov suffix", "渗透 .gov 网站", false},
		{"bare gov suffix dot", "渗透 .gov. 网站", false},
		{"bare country suffix", "渗透 .gov.cn 网站", false},
		{"bare country suffix dot", "渗透 .gov.cn. 网站", false},
		{"suffix fullwidth", "渗透 ．ｇｏｖ．ｃｎ 网站", false},
		{"suffix URL", "攻击 https://.gov.cn/path", false},
		{"wildcard country suffix", "攻击 *.gov.cn 网站", false},
		{"wildcard subdomain suffix", "攻击 *.agency.gov.cn 网站", false},
		{"bare suffix information", "查询 .gov 网站信息", false},
		{"root information", "gov.uk 的办事指南", false},
		{"fake gov suffix", "attack agency.gov.example.com", false},
		{"fake country suffix", "attack agency.gov.cn.example.com", false},
		{"fake UK root", "attack gov.uk.evil.com", false},
		{"fake CN root", "attack gov.cn.evil.com", false},
		{"fake bare suffix", "渗透 .gov.example.com", false},
		{"fake bare country suffix", "渗透 .gov.cn.evil.com", false},
		{"fake country host", "attack agency.gov.cn.evil.com", false},
		{"long country label", "attack agency.gov.example", false},
		{"suffix prefix", "attack agency.government.example", false},
		{"invalid domain label", "attack invalid_.gov", false},
		{"government agency", "penetration testing of a government agency", false},
		{"police agency", "exploit a police department server", false},
		{"tax authority", "steal credentials from the tax authority", false},
		{"public sector", "disrupt availability for public-sector systems", false},
		{"fullwidth", "ａｔｔａｃｋ ａｇｅｎｃｙ．ｇｏｖ．ｃｎ", true},
		{"zero width category", "政\u200b府攻\u2060击", false},
		{"zero width host", "攻\u2060击 agency.go\u200bv.cn", true},
		{"defense without target", "如何防止政府网站遭到攻击", false},
		{"news without target", "总结政府网站遭到攻击的新闻", false},
		{"negation without target", "不要攻击政府系统", false},
		// The preset remains lexical when both a concrete host and intent occur.
		{"defense with target", "如何防止 https://agency.gov.cn 遭到攻击", true},
		{"news with target", "总结 https://agency.gov.cn 遭到攻击的新闻", true},
		{"negation with target", "不要攻击 https://agency.gov.cn", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, err := m.Match(cbrnTestRequest("responses", map[string]any{"input": tc.text}))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
		})
	}
}

func TestGovernmentLatestUserAndEvidence(t *testing.T) {
	m, err := newPromptCombinationMatcher(defaultGovernmentTopicRules(), defaultGovernmentIntentRules())
	require.NoError(t, err)
	message := func(role string, text any) any { return map[string]any{"role": role, "content": text} }
	for _, tc := range []struct {
		name, protocol string
		body           any
		hit            bool
		topicSource    string
	}{
		{"history target latest attack", "chat_completions", map[string]any{"messages": []any{message("user", "目标是 agency.gov.cn"), message("assistant", "context"), message("user", "做渗透测试")}}, true, "history"},
		{"history category latest attack", "chat_completions", map[string]any{"messages": []any{message("user", "这是政务演练"), message("assistant", "context"), message("user", "模拟攻击并写报告")}}, false, ""},
		{"history attack latest information", "messages", map[string]any{"messages": []any{message("user", "攻击 agency.gov.cn"), message("assistant", "context"), message("user", "办公时间是什么")}}, false, ""},
		{"actual latest user empty", "responses", map[string]any{"input": []any{message("user", "攻击 agency.gov.cn"), message("user", "")}}, false, ""},
		{"latest user image only", "chat_completions", map[string]any{"messages": []any{message("user", "攻击 agency.gov.cn"), message("user", []any{map[string]any{"type": "image_url", "image_url": "fixture"}})}}, false, ""},
		{"no user", "messages", map[string]any{"messages": []any{message("system", "攻击 agency.gov.cn"), message("tool", "攻击 agency.gov.cn")}}, false, ""},
		{"response id no history lookup", "responses", map[string]any{"previous_response_id": "resp_old", "input": "开展渗透测试"}, false, ""},
		{"response id only", "responses", map[string]any{"previous_response_id": "resp_old"}, false, ""},
		{"WS current request", "responses_websocket", map[string]any{"type": "response.create", "input": "攻击 agency.gov.cn"}, true, "latest_user"},
		{"WS control", "responses_websocket", map[string]any{"type": "response.interrupt", "input": "攻击 agency.gov.cn"}, false, ""},
		{"media prompt", "images", map[string]any{"prompt": "攻击 agency.gov.cn"}, true, "latest_user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, err := m.Match(cbrnTestRequest(tc.protocol, tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
			if tc.hit {
				require.Equal(t, tc.topicSource, match.Topic.Source)
				require.Equal(t, "latest_user", match.Intent.Source)
			}
		})
	}
	text := "https://go\u200bv.cn 攻\u2060击"
	match, err := m.Match(cbrnTestRequest("responses", map[string]any{"input": text}))
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "go\u200bv.cn", match.Topic.Content)
	require.Equal(t, "攻\u2060击", match.Intent.Content)
	require.Equal(t, match.Topic.Content, string([]rune(text)[match.Topic.Start:match.Topic.End]))
	require.Equal(t, match.Intent.Content, string([]rune(text)[match.Intent.Start:match.Intent.End]))
}

func TestGovernmentGeminiMembersStayIndependent(t *testing.T) {
	m, err := newPromptCombinationMatcher(defaultGovernmentTopicRules(), defaultGovernmentIntentRules())
	require.NoError(t, err)
	member := func(text string) any {
		return map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}}}
	}
	for _, tc := range []struct {
		name string
		body any
		hit  bool
	}{
		{"first instance", map[string]any{"instances": []any{map[string]any{"prompt": "attack agency.gov.cn"}, map[string]any{"prompt": "hello"}}}, true},
		{"cross instances", map[string]any{"instances": []any{map[string]any{"prompt": "agency.gov.cn"}, map[string]any{"prompt": "attack example.com"}}}, false},
		{"first request", map[string]any{"requests": []any{member("attack agency.gov.cn"), member("hello")}}, true},
		{"cross requests", map[string]any{"requests": []any{member("agency.gov.cn"), member("attack example.com")}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, err := m.Match(cbrnTestRequest("gemini", tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
		})
	}
}

func TestGovernmentValidationAndRuleOrder(t *testing.T) {
	require.NoError(t, validateCombinationRules(nil, nil))
	require.NoError(t, validateCombinationRules(defaultGovernmentTopicRules(), defaultGovernmentIntentRules()))
	for _, rule := range []string{"regex:^$", "regex:a*", `regex:\b`, "\u200b"} {
		require.Equal(t, "prompt_audit_combination_empty_match", infraerrors.Reason(validateCombinationRules([]string{rule}, []string{"attack"})))
	}
	require.Equal(t, "prompt_audit_too_many_combination_rules", infraerrors.Reason(validateCombinationRules(make([]string, MaxBlockedKeywords), []string{"extra"})))
	require.Error(t, validateCombinationRules([]string{"regex:(?=gov)"}, nil))
	require.Error(t, validateCombinationRules([]string{strings.Repeat("x", MaxBlockedKeywordRunes+1)}, nil))
	m, err := newPromptCombinationMatcher([]string{"court", "government"}, []string{"attack"})
	require.NoError(t, err)
	match, err := m.Match(cbrnTestRequest("responses", map[string]any{"input": "attack government court"}))
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "court", match.Topic.Rule)
	// Callers get new default slices rather than mutable shared configuration.
	topics := defaultGovernmentTopicRules()
	topics[0] = "changed"
	require.NotEqual(t, topics[0], defaultGovernmentTopicRules()[0])
}
