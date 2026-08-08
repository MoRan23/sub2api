package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/keywordmatcher"

type contentModerationKeywordMatcher struct {
	matcher *keywordmatcher.Matcher
}

func newContentModerationKeywordMatcher(keywords []string) *contentModerationKeywordMatcher {
	matcher := keywordmatcher.New(keywords)
	if matcher == nil {
		return nil
	}
	return &contentModerationKeywordMatcher{matcher: matcher}
}

func (m *contentModerationKeywordMatcher) Match(text string) (string, bool) {
	if m == nil {
		return "", false
	}
	return m.matcher.Match(text)
}
