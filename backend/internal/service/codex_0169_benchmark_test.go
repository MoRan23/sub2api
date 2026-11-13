package service

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Identical fixtures are run against the pre-adaptation tree and the current
// tree. Payloads contain no real prompts, credentials, or upstream traffic.
func BenchmarkCodex0169Compatibility(b *testing.B) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := http.Header{"X-Codex-Guardian": {"reviewer"}}
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"` + strings.Repeat("a", size) + `"}],"client_metadata":{"guardian_credits_requested":"true","parent_response_id":"resp_bench","x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"turn_trigger\":\"retry\"}"}}`)
		b.Run(fmt.Sprintf("normalize/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				profile := captureCodexWireProfile(nil, body, "")
				if _, err := profile.MarshalNestedJSON(true); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("fingerprint/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				_ = buildFingerprintObservationEntry(nil, account, installationIDResolution{}, headers, body, OpenAICodexTurnIdentity{}, false, true)
			}
		})
	}
}
