package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Run identically before and after changes. Fixtures contain synthetic text only;
// image payloads verify that audit work does not retain non-text request data.
func BenchmarkPromptAuditMemory(b *testing.B) {
	for _, kind := range []string{"text", "image"} {
		for _, size := range []int{64 << 10, 1 << 20, 4 << 20, 8 << 20} {
			input := []any{map[string]any{"role": "user", "content": "hello"}}
			if kind == "text" {
				input = append([]any{map[string]any{"role": "user", "content": strings.Repeat("safe text ", size/10)}, map[string]any{"role": "assistant", "content": "context"}}, input...)
			} else {
				input = append(input, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", size)}}})
			}
			body, err := json.Marshal(map[string]any{"model": "test-model", "input": input})
			if err != nil {
				b.Fatal(err)
			}
			for _, mode := range []string{"off", "keyword", "combination", "snapshot"} {
				b.Run(fmt.Sprintf("%s/%d/%s", kind, size, mode), func(b *testing.B) {
					cfg := storageConfig{KeywordAllGroups: true, AllGroups: true, BlockedKeywords: []string{"blocked-word"}}
					cfg.KeywordBlockingEnabled = mode == "keyword" || mode == "combination"
					if mode == "combination" {
						for i := range 8 {
							cfg.CombinationPolicies = append(cfg.CombinationPolicies, CombinationPolicy{ID: fmt.Sprintf("policy%d", i), Name: "test", Enabled: true, TopicRules: []string{"orchid"}, IntentRules: []string{"prune"}})
						}
					}
					active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
					if err != nil {
						b.Fatal(err)
					}
					service := &PromptService{config: &fakeConfigStore{active: true, cfg: active}}
					coordinator := NewCoordinator(nil, service)
					req := Request{Protocol: "responses", Body: body}
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					b.ResetTimer()
					for b.Loop() {
						if mode == "snapshot" {
							if _, err := ExtractPromptSnapshot(req); err != nil {
								b.Fatal(err)
							}
						} else if decision := coordinator.Check(context.Background(), req); !decision.AllowNextStage {
							b.Fatal(decision.Kind)
						}
					}
				})
			}
		}
	}
}
