package securityaudit

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// Keep this benchmark compatible with the pre-path-filter implementation so an
// isolated archive can run exactly the same synthetic inputs and policy set.
// Intent matches the latest user text, forcing topic evaluation over the large
// segment. No topic matches in either implementation.
func BenchmarkPromptCombinationFilter(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 4 << 20, 8 << 20} {
		for _, scenario := range []struct{ name, text string }{
			{"plain", "ordinary discussion text "},
			{"filesystem", `C:\workspace\notes\report.txt /var/tmp/notes/report.txt `},
			{"shortpath", "/a "},
			{"url", "https://example.invalid/reports/log.txt?user=reader "},
		} {
			b.Run(fmt.Sprintf("%s/%d", scenario.name, size), func(b *testing.B) {
				text := strings.Repeat(scenario.text, size/len(scenario.text)) + " prune"
				body, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": text}}})
				if err != nil {
					b.Fatal(err)
				}
				cfg := storageConfig{KeywordAllGroups: true, AllGroups: true, KeywordBlockingEnabled: true}
				for index := range 8 {
					cfg.CombinationPolicies = append(cfg.CombinationPolicies, CombinationPolicy{
						ID: fmt.Sprintf("policy%d", index), Name: "synthetic policy", Enabled: true,
						TopicRules: []string{"orchid"}, IntentRules: []string{"prune"},
					})
				}
				active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
				if err != nil {
					b.Fatal(err)
				}
				req := Request{Protocol: "responses", Body: body}
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for b.Loop() {
					matched, err := active.MatchCombination(req)
					if err != nil || matched != nil {
						b.Fatalf("unexpected match: %+v, error: %v", matched, err)
					}
					runtime.KeepAlive(matched)
				}
			})
		}
	}
}
