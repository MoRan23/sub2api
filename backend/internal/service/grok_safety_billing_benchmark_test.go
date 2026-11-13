package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var grokSafetySnapshotSink *OpenAIRecordUsageInput

// The refusal adds bounded evidence to the same billing-only snapshot. Replay
// data is intentionally never retained, irrespective of the upstream result size.
func BenchmarkGrokSafetyUsageSnapshot(b *testing.B) {
	for _, size := range []int{0, 8 << 20} {
		for _, rejection := range []bool{false, true} {
			b.Run(fmt.Sprintf("replay=%d/rejection=%t", size, rejection), func(b *testing.B) {
				key, user, account, sub := usageSnapshotTestSubjects()
				result := &OpenAIForwardResult{RequestID: "request-id", Model: "grok-test", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 100}}
				if size > 0 {
					result.wsReplayInput = []json.RawMessage{json.RawMessage(`{"text":"` + strings.Repeat("x", size) + `"}`)}
				}
				if rejection {
					result.GrokSafetyRejection = &GrokSafetyRejection{CallID: "server-call-id", Code: GrokSafetyRejectionCode, Fee: .25, HTTPStatus: 403, UpstreamRequestID: "upstream-id"}
				}
				input := &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user, Account: account, Subscription: sub}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					grokSafetySnapshotSink = SnapshotOpenAIRecordUsageInput(input)
				}
			})
		}
	}
}
