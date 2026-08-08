package keywordmatcher

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatcherIsCaseInsensitiveAndPreservesConfiguredOrder(t *testing.T) {
	matcher := New([]string{"Needle", "needle in a haystack", "密钥"})
	matched, ok := matcher.Match("A NEEDLE in a haystack and 密钥")
	require.True(t, ok)
	require.Equal(t, "Needle", matched)

	matched, ok = matcher.Match("安全密钥")
	require.True(t, ok)
	require.Equal(t, "密钥", matched)
}

func TestMatcherHandlesOverlappingKeywordsAndMisses(t *testing.T) {
	matcher := New([]string{"abcd", "bc", "c"})
	matched, ok := matcher.Match("xxabxx")
	require.False(t, ok)
	require.Empty(t, matched)

	matched, ok = matcher.Match("xxabcdxx")
	require.True(t, ok)
	require.Equal(t, "abcd", matched)
}

func TestMatcherIsSafeForConcurrentMatches(t *testing.T) {
	matcher := New([]string{"alpha", "beta", "gamma"})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			text := "nothing"
			want := ""
			if index%3 == 0 {
				text, want = "ALPHA", "alpha"
			} else if index%3 == 1 {
				text, want = "beta", "beta"
			}
			matched, ok := matcher.Match(text)
			if want == "" {
				require.False(t, ok)
				return
			}
			require.True(t, ok)
			require.Equal(t, want, matched)
		}(i)
	}
	wg.Wait()
}
