package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Uses only the pre-0.170 public types so the identical fixture can also run
// against the baseline revision. No audio is silently discarded on either side.
func BenchmarkCodex0170ExistingChatBridge(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			input, err := json.Marshal([]map[string]any{
				{"role": "user", "content": strings.Repeat("x", size)},
				{"type": "function_call", "name": "lookup", "call_id": "call_1", "arguments": "{}"},
				{"type": "function_call_output", "call_id": "call_1", "output": "result"},
			})
			if err != nil {
				b.Fatal(err)
			}
			request := &ResponsesRequest{Model: "gpt-6-sol", Input: input, Reasoning: &ResponsesReasoning{Effort: "high"}}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				out, err := ResponsesToChatCompletionsRequest(request)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := json.Marshal(out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
