//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func BenchmarkOpenAIPassthroughErrorSanitizer(b *testing.B) {
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	if err := json.Unmarshal([]byte(`{"api_key":"sk-business-secret","access_token":"access-secret","refresh_token":"refresh-secret","base_url":"https://relay.example.invalid/v1"}`), &account.Credentials); err != nil {
		b.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "https://physical.example.invalid/v1/responses", nil)
	if err != nil {
		b.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer sk-physical-secret")
	for _, size := range []int{4 << 10, 512 << 10} {
		for _, fixture := range []struct {
			name   string
			prefix string
		}{
			{"no_match", "upstream rejected request: "},
			{"redactions", "relay.example.invalid physical.example.invalid sk-business-secret sk-physical-secret access-secret refresh-secret?api_key=query-secret "},
		} {
			b.Run(fmt.Sprintf("%dKiB/%s", size>>10, fixture.name), func(b *testing.B) {
				message := fixture.prefix + strings.Repeat("x", size-len(fixture.prefix))
				b.SetBytes(int64(len(message)))
				b.ReportAllocs()
				for b.Loop() {
					sanitizer := newOpenAIPassthroughErrorSanitizer(account, request)
					sanitizer.sanitize(message)
					sanitizer.sanitize("invalid_request_error")
					sanitizer.sanitize("context_length_exceeded")
					sanitizer.sanitize("input")
				}
			})
		}
	}
}
