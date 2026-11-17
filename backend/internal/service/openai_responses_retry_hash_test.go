package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesRetryLazyHashNormalPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"input":"large immutable input"}`)
	for range 15 {
		state := openAIResponsesRejectedFieldRetryStateForRequest(c, body)
		require.Nil(t, state.seenBodyHashes, "ordinary sends and 429 retries need no compatibility digests")
		require.Equal(t, 0, state.budget.attempts)
		require.False(t, state.Allow(nil))
		require.Nil(t, state.seenBodyHashes)
	}
	// A WebSocket session may outlive its payload, so its guard stays eager.
	wsState := newOpenAIResponsesRejectedFieldRetryState(body)
	require.Nil(t, wsState.initialBody)
	require.False(t, wsState.Allow(body))
}

func TestOpenAIResponsesRetryLazyHashMatchesEager(t *testing.T) {
	for _, initial := range [][]byte{nil, []byte{}, []byte(`{"initial":"body"}`)} {
		t.Run(fmt.Sprintf("initial_%d", len(initial)), func(t *testing.T) {
			eagerBudget, lazyBudget := &openAIResponsesRejectedFieldRetryBudget{}, &openAIResponsesRejectedFieldRetryBudget{}
			// Several account attempts share one budget but have independent guards.
			for account := range 3 {
				eager := newOpenAIResponsesRejectedFieldRetryStateWithBudget(initial, eagerBudget)
				lazy := newLazyOpenAIResponsesRejectedFieldRetryState(initial, lazyBudget)
				for _, candidate := range [][]byte{nil, initial, []byte(`{"next":1}`), []byte(`{"next":1}`), initial} {
					require.Equal(t, eager.Allow(candidate), lazy.Allow(candidate))
				}
				// Invalid-encrypted-content recovery remembers a body before the
				// rejected-field guard ever gets Allow; the original remains seen.
				recovered := []byte(fmt.Sprintf(`{"recovery":%d}`, account))
				eager.remember(recovered)
				lazy.remember(recovered)
				require.Nil(t, lazy.initialBody, "after activation only hashes remain")
				for _, candidate := range [][]byte{recovered, initial, []byte(`{"next":2}`), []byte(`{"next":3}`), []byte(`{"next":4}`)} {
					require.Equal(t, eager.Allow(candidate), lazy.Allow(candidate))
				}
				require.Equal(t, eagerBudget.attempts, lazyBudget.attempts)
				require.Equal(t, eager.seenBodyHashes, lazy.seenBodyHashes)
			}
		})
	}
}

func TestOpenAIResponsesRetryLazyHashRememberFirst(t *testing.T) {
	initial, recovered := []byte(`{"initial":1}`), []byte(`{"recovered":1}`)
	s := newLazyOpenAIResponsesRejectedFieldRetryState(initial, &openAIResponsesRejectedFieldRetryBudget{})
	s.remember(recovered)
	require.Nil(t, s.initialBody)
	require.False(t, s.Allow(initial))
	require.False(t, s.Allow(recovered))
	require.True(t, s.Allow([]byte(`{"next":1}`)))
}

func TestOpenAIResponsesRetryLazyHashConcurrentActivation(t *testing.T) {
	initial, next := []byte(`{"initial":1}`), []byte(`{"next":1}`)
	s := newLazyOpenAIResponsesRejectedFieldRetryState(initial, &openAIResponsesRejectedFieldRetryBudget{})
	var wg sync.WaitGroup
	results := make(chan bool, 20)
	for range 20 {
		wg.Go(func() { results <- s.Allow(next) })
	}
	wg.Wait()
	close(results)
	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}
	require.Equal(t, 1, allowed)
	require.False(t, s.Allow(initial))
	require.Nil(t, s.initialBody)
}

var benchmarkOpenAIRetryState *openAIResponsesRejectedFieldRetryState

func BenchmarkOpenAIResponsesRetryLazyHash(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		body := []byte(`{"input":"` + strings.Repeat("x", size) + `"}`)
		for _, attempts := range []int{1, 5, 15} {
			for _, lazy := range []bool{false, true} {
				name := "before"
				if lazy {
					name = "after"
				}
				b.Run(fmt.Sprintf("%dKiB/%dattempts/%s", size>>10, attempts, name), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						budget := &openAIResponsesRejectedFieldRetryBudget{}
						for range attempts {
							if lazy {
								benchmarkOpenAIRetryState = newLazyOpenAIResponsesRejectedFieldRetryState(body, budget)
							} else {
								benchmarkOpenAIRetryState = newOpenAIResponsesRejectedFieldRetryStateWithBudget(body, budget)
							}
						}
					}
				})
			}
		}
	}
}
