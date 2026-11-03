package securityaudit

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/keywordmatcher"
)

const promptKeywordRegexPrefix = "regex:"

type promptKeywordRegex struct {
	index int
	rule  string
	re    *regexp.Regexp
}

// promptKeywordMatcher is immutable after snapshot construction. Literal rules
// retain the shared Aho-Corasick fast path; regex rules are compiled once and
// checked only while they can outrank the first literal match.
type promptKeywordMatcher struct {
	literals     *keywordmatcher.Matcher
	literalOrder map[string]int
	regexes      []promptKeywordRegex
}

func newPromptKeywordMatcher(rules []string) (*promptKeywordMatcher, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	matcher := &promptKeywordMatcher{literalOrder: make(map[string]int, len(rules))}
	literals := make([]string, 0, len(rules))
	for index, rule := range rules {
		if strings.HasPrefix(rule, promptKeywordRegexPrefix) {
			re, err := compilePromptKeywordRegex(rule, index)
			if err != nil {
				return nil, err
			}
			matcher.regexes = append(matcher.regexes, promptKeywordRegex{index: index, rule: rule, re: re})
			continue
		}
		if rule == "" {
			continue
		}
		if _, exists := matcher.literalOrder[rule]; !exists {
			matcher.literalOrder[rule] = index
		}
		literals = append(literals, rule)
	}
	matcher.literals = keywordmatcher.New(literals)
	if matcher.literals == nil && len(matcher.regexes) == 0 {
		return nil, nil
	}
	return matcher, nil
}

func compilePromptKeywordRegex(rule string, index int) (*regexp.Regexp, error) {
	if utf8.RuneCountInString(rule) > MaxBlockedRegexRunes {
		return nil, infraerrors.BadRequest("prompt_audit_keyword_regex_too_long", "提示词审计正则规则长度超出允许范围")
	}
	expression := strings.TrimPrefix(rule, promptKeywordRegexPrefix)
	if strings.TrimSpace(expression) != "" {
		if compiled, err := regexp.Compile(expression); err == nil {
			return compiled, nil
		}
	}
	// Identify the rule without reflecting its contents or the full config.
	return nil, infraerrors.BadRequest("prompt_audit_invalid_keyword_regex", fmt.Sprintf("第 %d 条关键词规则的正则表达式无效，请使用 Go/RE2 支持的语法", index+1))
}

func (m *promptKeywordMatcher) Match(text string) (string, bool) {
	if m == nil || text == "" {
		return "", false
	}
	literal, literalHit := m.literals.Match(text)
	for _, candidate := range m.regexes {
		if literalHit && candidate.index >= m.literalOrder[literal] {
			break
		}
		if candidate.re.MatchString(text) {
			return candidate.rule, true
		}
	}
	return literal, literalHit
}

func promptKeywordEvidenceText(rule string) string {
	if strings.HasPrefix(rule, promptKeywordRegexPrefix) {
		return rule
	}
	return strings.ToLower(strings.TrimSpace(rule))
}
