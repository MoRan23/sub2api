package securityaudit

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestCombinationPathsReferenceCases(t *testing.T) {
	rules, err := compileCombinedRules(append(defaultGovernmentTopicRules(), defaultGovernmentIntentRules()...), "invalid")
	require.NoError(t, err)
	for _, tc := range []struct {
		text string
		want [3]bool
	}{
		{`请攻击 C:\资料\政府工作报告.txt`, [3]bool{false, false, true}},
		{`/资料/政府工作报告.txt`, [3]bool{}},
		{`/资料/针对公安的攻击记录.txt`, [3]bool{}},
		{`C:\资料\www.gov.cn\攻击记录.txt`, [3]bool{}},
		{`\\server\政府\attack.txt`, [3]bool{}},
		{`"C:\工作 资料\政府 攻击报告.txt"`, [3]bool{}},
		{`'/home/政府 工作/攻击.txt'`, [3]bool{}},
		{`logs/政府攻击报告.txt`, [3]bool{}},
		{`攻击记录.txt`, [3]bool{}},
		{`government.txt attack.txt`, [3]bool{}},
		{`https://www.gov.cn/政府/攻击.html`, [3]bool{true, false, false}},
		{`www.gov.cn`, [3]bool{true, false, false}},
		{`gov.cn .gov.cn`, [3]bool{true, false, false}},
		{`www.gov.cn/path`, [3]bool{}},
		{`www.gov.uk attack`, [3]bool{false, false, true}},
		{`https://example.com/www.gov.cn/政府/攻击`, [3]bool{}},
		{`https://www.gov.cn.evil.com/`, [3]bool{}},
		{`政府要求禁止攻击 www.gov.cn`, [3]bool{true, true, true}},
		{`government attack`, [3]bool{false, true, true}},
		{`窃取 /tmp/f 账号`, [3]bool{}},
		{`破坏 /tmp/f 服务`, [3]bool{}},
		{`steal /tmp/f passwords`, [3]bool{}},
		{`窃取账号 /tmp/密码`, [3]bool{false, false, true}},
		{`拖库 撞庫`, [3]bool{false, false, true}},
		{`政府。www.gov.cn。攻击`, [3]bool{true, true, true}},
		{`/tmp/政府攻击.txt。攻击`, [3]bool{false, false, true}},
		{`政府工作报告.备份`, [3]bool{}},
		{`请攻击　C:\资料\政府工作报告.txt`, [3]bool{false, false, true}},
		{`"请攻击 /tmp/政府报告.txt"`, [3]bool{false, false, true}},
		{`"请攻击 https://www.gov.cn/政府报告.txt"`, [3]bool{true, false, true}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			segment := cbrnTextSegment{normalized: normalizeCBRNText(tc.text)}
			segment.spans = combinationTextSpans(segment.normalized)
			var got [3]bool
			for index, rule := range rules {
				got[index] = segment.find(rule.re) != nil
			}
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCombinationPathsHTTPHostOffsets(t *testing.T) {
	for _, tc := range []struct {
		text, host string
	}{
		{`https://www.gov.cn/政府/attack?token=secret#fragment`, "www.gov.cn"},
		{`HTTPS://user:password@WWW.GOV.CN:8443/政府/attack`, "WWW.GOV.CN"},
		{`http://127.0.0.1:8080/path?gov.cn`, "127.0.0.1"},
		{`http://[2001:db8::1]:8080/path`, "2001:db8::1"},
		{`http://[::1]`, "::1"},
		{`[https://[::1]]`, "::1"},
		{`https://[fe80::1%25eth0]:8443/path`, "fe80::1%25eth0"},
		{`"https://user:pass%40word@www.gov.cn:443/secret"`, "www.gov.cn"},
		{`<https://www.gov.cn>`, "www.gov.cn"},
		{`(https://www.gov.cn).`, "www.gov.cn"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			view := combinationTextSpans(tc.text)
			require.NotNil(t, view)
			require.Equal(t, 1, view.spanCount)
			iterator := view.iterator()
			span, ok := iterator.next()
			require.True(t, ok)
			require.Equal(t, tc.host, tc.text[span.start:span.end])
		})
	}
	for _, text := range []string{`https://:443/政府/attack`, `https://bad%00host/政府/attack`, `https://[broken/政府/attack`, `www.gov.cn/attack`} {
		view := combinationTextSpans(text)
		require.NotNil(t, view, text)
		require.Zero(t, view.spanCount, text)
	}
}

func TestCombinationPathsHardBoundariesAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, text, topic, intent string
		hit                       bool
	}{
		{"topic cannot bridge", "政府 /tmp/file 机构 attack", `regex:(?s)政府.*机构`, "attack", false},
		{"intent cannot bridge", "政府 窃取 /tmp/file 账号", "政府", `regex:(?s)窃取.*账号`, false},
		{"whitespace cannot bridge", "government steal /tmp/file passwords", "government", `regex:steal[\s\S]*passwords`, false},
		{"separate conditions can surround path", "government /tmp/file attack", "government", "attack", true},
		{"URL host isolated left", "before https://www.gov.cn/path attack", `regex:before.*gov`, "attack", false},
		{"URL host isolated right", "https://www.gov.cn attack", `regex:gov.*attack`, "attack", false},
		// Each retained region is a standalone regex input. Anchors refer to
		// that region rather than an artificial NUL-inclusive rewritten body.
		{"anchors target isolated hostname", "attack https://www.gov.cn/path", `regex:^www\.gov\.cn$`, "attack", true},
		{"CBRN filename excluded", "make /tmp/sarin.txt", "sarin", "make", false},
		{"CBRN filename intent excluded", "sarin manufacture.txt", "sarin", "manufacture", false},
		{"CBRN URL host allowed", "make https://sarin.example/path", "sarin", "make", true},
		{"custom literals", "topic.txt intent", "topic", "intent", false},
		{"quoted sentence", `"attack /tmp/report.txt government"`, "government", "attack", true},
		{"quoted sentence suffix", `"read /tmp/government.txt" attack`, "government", "attack", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matcher, err := newPromptCombinationMatcher([]string{tc.topic}, []string{tc.intent})
			require.NoError(t, err)
			match, err := matcher.Match(cbrnTestRequest("responses", map[string]any{"input": tc.text}))
			require.NoError(t, err)
			require.Equal(t, tc.hit, match != nil)
		})
	}

	text := `路径 /tmp/政府攻击.txt。 ＡＴＴＡＣＫ HTTPS://user:password@ｗｗｗ．ｇｏｖ．ｃｎ:443/攻击`
	matcher, err := newPromptCombinationMatcher([]string{`regex:(?i)www\.gov\.cn`}, []string{"attack"})
	require.NoError(t, err)
	req := cbrnTestRequest("responses", map[string]any{"input": text})
	before := string(req.Body)
	match, err := matcher.Match(req)
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "ｗｗｗ．ｇｏｖ．ｃｎ", match.Topic.Content)
	require.Equal(t, "ＡＴＴＡＣＫ", match.Intent.Content)
	for _, evidence := range []CBRNMatchEvidence{match.Topic, match.Intent} {
		require.Equal(t, evidence.Content, string([]rune(text)[evidence.Start:evidence.End]))
	}
	require.Equal(t, before, string(req.Body))
}

func TestCombinationPathsHistoryAndSharedView(t *testing.T) {
	matcher, err := newPromptCombinationMatcher([]string{"sarin"}, []string{"make"})
	require.NoError(t, err)
	message := func(text string) any { return map[string]any{"role": "user", "content": text} }
	for _, history := range []string{`C:\work\sarin.txt`, `"/tmp/path with sarin.txt"`, `https://example.com/sarin`} {
		match, err := matcher.Match(cbrnTestRequest("responses", map[string]any{"input": []any{message(history), message("make")}}))
		require.NoError(t, err)
		require.Nil(t, match, history)
	}
	req := cbrnTestRequest("responses", map[string]any{"input": []any{message("sarin"), message("make /tmp/file")}})
	prepared, err := preparePromptCombination(req)
	require.NoError(t, err)
	first := matcher.matchPrepared(prepared)
	require.NotNil(t, first)
	require.Equal(t, "history", first.Topic.Source)
	require.Equal(t, "latest_user", first.Intent.Source)
	spans := prepared.batches[0][1].spans
	require.NotNil(t, spans)
	second := matcher.matchPrepared(prepared)
	require.Equal(t, first, second)
	require.Same(t, spans, prepared.batches[0][1].spans, "later policies must reuse the same filtered view")
}

func TestCombinationPathsDenseShortPathsKeepWhitespaceCompactly(t *testing.T) {
	const paths = 10000
	text := strings.Repeat("/a ", paths)
	view := combinationTextSpans(text)
	require.NotNil(t, view)
	require.Equal(t, paths, view.spanCount)
	bytes := view.inlineUsed
	for block := view.head; block != nil; block = block.next {
		bytes += block.used
	}
	require.Equal(t, 2*paths, bytes, "each whitespace span needs only skip+keep lengths")
	iterator := view.iterator()
	for index := range paths {
		span, ok := iterator.next()
		require.True(t, ok)
		require.Equal(t, combinationTextSpan{start: index*3 + 2, end: index*3 + 3}, span)
	}
	_, ok := iterator.next()
	require.False(t, ok)
	segment := cbrnTextSegment{normalized: text, spans: view}
	require.NotNil(t, segment.find(regexp.MustCompile(`^\s$`)))
	require.Nil(t, segment.find(regexp.MustCompile(`\s{2}`)), "whitespace on opposite sides of a path remains separate")
	require.Less(t, testing.AllocsPerRun(3, func() { combinationTextSpans(text) }), float64(10), "no per-path allocation or growing full-span slice")
}

func TestCombinationPathSpanEncodingCrossesBlockBoundaries(t *testing.T) {
	var view combinationTextView
	want := make([]combinationTextSpan, 0, 6000)
	position := 0
	for index := range 6000 {
		skip := []int{0, 1, 127, 128, 16384}[index%5]
		keep := []int{1, 127, 128, 16384, 65536}[index%5]
		span := combinationTextSpan{start: position + skip, end: position + skip + keep}
		view.appendSpan(span.start, span.end)
		want = append(want, span)
		position = span.end
	}
	iterator := view.iterator()
	for _, expected := range want {
		span, ok := iterator.next()
		require.True(t, ok)
		require.Equal(t, expected, span)
	}
	_, ok := iterator.next()
	require.False(t, ok)
}

func TestCombinationHTTPHostFastPathMatchesURLParser(t *testing.T) {
	for _, value := range []string{
		"http://gov.cn", "HTTPS://WWW.GOV.CN:8443/path?query=value#fragment", "https://user:secret@www.gov.cn/path",
		"http://127.0.0.1:8080/", "https://www.gov.cn:", "https://www.gov.cn:bad/path", "https://www.gov.cn:999999999999/path",
		"https://[2001:db8::1]:443/path", "https://[broken/path", "https://[::1]:bad/path", "https://user%xx@gov.cn/",
		"https://bad%00host/path", "https://www.gov.cn/%xx", "https://www.gov.cn/#%xx", "https://www.gov.cn/?%xx",
		"https://www.gov.cn/%41?invalid=%xx#%42", "https://www.gov.cn/?%#%42", "https://www.gov.cn/%3f%23?%xx#%3f%23",
		"https://www.gov.cn/#%", "https://www.gov.cn/#%0", "https://www.gov.cn/?%", "https://www.gov.cn/?x=%xx#%xx",
		"https://www.gov.cn/%00", "https://www.gov.cn/#%00", "https://www.gov.cn/?\x00", "https://www.gov.cn/#\x00",
		"https://www.gov.cn/路径", "https://www.gov.cn/with space", "https://www.gov.cn/\x00", "https://www.gov.cn/\x7f",
		"https://www.gov.cn\\evil/path", "https://www.gov.cn\x01/path", "https://www.gov.cn:4:3/path", "https://", "http:///path",
	} {
		t.Run(value, func(t *testing.T) {
			parsed, err := url.Parse(value)
			start, end, ok := combinationHTTPHost(value)
			valid := err == nil && parsed.Hostname() != ""
			require.Equal(t, valid, ok)
			if ok {
				require.Equal(t, parsed.Hostname(), value[start:end])
			}
		})
	}
	common := "https://example.invalid:443/reports/log.txt?user=reader"
	require.Zero(t, testing.AllocsPerRun(100, func() {
		_, _, ok := combinationHTTPHost(common)
		if !ok {
			panic("valid URL rejected")
		}
	}))
}

func TestCombinationHTTPHostDoesNotMaterializeEscapedSuffix(t *testing.T) {
	for _, part := range []string{"/", "#", "?query="} {
		suffix := part + strings.Repeat("%41", 1<<18)
		for _, authority := range []string{"https://www.gov.cn", "https://reader%40name:secret@[fe80::1%25eth0]:443"} {
			small := authority + part + "%41"
			large := authority + suffix
			allocations := func(value string) float64 {
				return testing.AllocsPerRun(3, func() {
					_, _, ok := combinationHTTPHost(value)
					if !ok {
						panic("valid escaped URL rejected")
					}
				})
			}
			require.Equal(t, allocations(small), allocations(large), authority+part)
			if authority == "https://www.gov.cn" {
				require.Zero(t, allocations(large), "the entire escaped suffix remains a read-only slice")
			}
			parsed, err := url.Parse(large)
			require.NoError(t, err)
			require.NotEmpty(t, parsed.Hostname())
		}
	}
}

func TestCombinationPathsChineseProseAdjacentToGovernmentHost(t *testing.T) {
	matcher, err := newPromptCombinationMatcher(defaultGovernmentTopicRules(), defaultGovernmentIntentRules())
	require.NoError(t, err)
	for _, text := range []string{"请攻击www.gov.cn", "攻击 www.gov.cn网站", "请攻击www.gov.cn网站", "渗透.gov.cn网站", "请攻击ｗｗｗ．ｇｏｖ．ｃｎ网站"} {
		match, err := matcher.Match(cbrnTestRequest("responses", map[string]any{"input": text}))
		require.NoError(t, err)
		require.NotNil(t, match, text)
	}
	for _, text := range []string{
		"请攻击www.gov.cn.evil", "攻击 www.gov.cn.备份", "攻击 gov.cn.备份", "攻击 政府工作报告.备份",
		"请攻击www.gov.cn_/file", "攻击 /tmp/请访问www.gov.cn网站", `攻击 C:\请访问www.gov.cn网站`,
		"请攻击www.gov.cn_evil", "请攻击www.gov.cn.evil网站", "攻击 gov.cn.政府报告",
	} {
		match, err := matcher.Match(cbrnTestRequest("responses", map[string]any{"input": text}))
		require.NoError(t, err)
		require.Nil(t, match, text)
	}
}

func TestCombinationPathsNoFilterAllocationAndConcurrentRequests(t *testing.T) {
	text := strings.Repeat("plain text without path markers ", 1024)
	require.Zero(t, testing.AllocsPerRun(20, func() { require.Nil(t, combinationTextSpans(text)) }))
	matcher, err := newPromptCombinationMatcher([]string{"sarin"}, []string{"make"})
	require.NoError(t, err)
	var work sync.WaitGroup
	for range 8 {
		work.Go(func() {
			for range 20 {
				match, err := matcher.Match(cbrnTestRequest("responses", map[string]any{"input": "make /tmp/sarin.txt"}))
				require.NoError(t, err)
				require.Nil(t, match)
			}
		})
	}
	work.Wait()
}

func TestCombinationPathsDoNotChangeStandaloneKeywords(t *testing.T) {
	for _, rule := range []string{"sarin", "regex:(?i)sarin"} {
		standalone, err := newPromptKeywordMatcher([]string{rule})
		require.NoError(t, err)
		text := `make C:\work\sarin.txt`
		matchedRule, content, matched := standalone.Match(text)
		require.True(t, matched, "jailbreak and other standalone keywords must keep their existing path behavior")
		require.Equal(t, rule, matchedRule)
		require.Equal(t, "sarin", content)
		combined, err := newPromptCombinationMatcher([]string{rule}, []string{"make"})
		require.NoError(t, err)
		match, err := combined.Match(cbrnTestRequest("responses", map[string]any{"input": text}))
		require.NoError(t, err)
		require.Nil(t, match)
	}
}

func TestCombinationPathsZeroWidthFilenameAndQuotedWhitespace(t *testing.T) {
	for _, text := range []string{
		`make C:sa` + "\u200b" + `rin.txt`,
		`make C:\ＳＡＲＩＮ.txt`,
		"make \"C:\\work space\\sarin.txt\"",
		"make '/tmp/sarin report.txt'",
		"make `~/sarin report.txt`",
	} {
		matcher, err := newPromptCombinationMatcher([]string{"sarin"}, []string{"make"})
		require.NoError(t, err)
		match, err := matcher.Match(cbrnTestRequest("responses", map[string]any{"input": text}))
		require.NoError(t, err)
		require.Nil(t, match, text)
	}
	// Evidence after a removed prefix retains both normalized and original
	// offsets, including zero-width characters within the actual host.
	text := `discard /tmp/old.txt attack https://www.go` + "\u200b" + `v.cn/path`
	m, err := newPromptCombinationMatcher([]string{`regex:www\.gov\.cn`}, []string{"attack"})
	require.NoError(t, err)
	match, err := m.Match(cbrnTestRequest("responses", map[string]any{"input": text}))
	require.NoError(t, err)
	require.NotNil(t, match)
	require.Equal(t, "www.go\u200bv.cn", match.Topic.Content)
	require.Equal(t, utf8.RuneCountInString(text[:strings.Index(text, "www.go")]), match.Topic.Start)
	require.Regexp(t, regexp.QuoteMeta("www.go\u200bv.cn"), match.Topic.Content)
}
