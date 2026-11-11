package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func originalRedactPreview(value string, maxRunes int) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer ***")
	value = apiKeyPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, ":= \t"); index >= 0 {
			return match[:index+1] + "***"
		}
		return "***"
	})
	value = canaryPattern.ReplaceAllString(value, "${1}***")
	value = emailPattern.ReplaceAllString(value, "***@***")
	value = phonePattern.ReplaceAllString(value, "***PHONE***")
	return originalTrimRunes(value, maxRunes)
}

// Alternate order inside the same sample to distinguish host scheduling noise
// from redaction CPU changes. Both implementations produce identical summaries.
func BenchmarkPromptRedactionPaired(b *testing.B) {
	input := strings.Repeat("ordinary text ", (8<<20)/14)
	var oldTime, newTime time.Duration
	for b.Loop() {
		started := time.Now()
		originalRedactPreview(input, 96)
		oldTime += time.Since(started)
		started = time.Now()
		RedactPreview(input, 96)
		newTime += time.Since(started)
		started = time.Now()
		RedactPreview(input, 96)
		newTime += time.Since(started)
		started = time.Now()
		originalRedactPreview(input, 96)
		oldTime += time.Since(started)
	}
	b.ReportMetric(float64(oldTime.Nanoseconds())/float64(2*b.N), "old-ns/call")
	b.ReportMetric(float64(newTime.Nanoseconds())/float64(2*b.N), "new-ns/call")
}

func TestPromptRedactionPreservesWholeInputSemantics(t *testing.T) {
	for _, input := range []string{
		"", "short secret", strings.Repeat("ordinary text ", 100),
		"Bearer " + strings.Repeat("s", 300) + " tail",
		strings.Repeat("x", 94) + " api_key=" + strings.Repeat("s", 200),
		"test@example.invalid +1 123 456 7890 TEST_CANARY_secret api_key=exampleSECRET123",
		"\xffBearer exampleTOKEN123\xfe 中文😀 test@example.invalid",
		strings.Repeat("Bearer exampleTOKEN123 ", 100),
	} {
		for _, limit := range []int{-1, 0, 1, 31, 32, 96, 65536} {
			got, want := RedactPreview(input, limit), originalRedactPreview(input, limit)
			if got != want {
				t.Fatalf("limit=%d: got %q, want %q", limit, got, want)
			}
		}
	}
}

func BenchmarkPromptRedactionMemory(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		for _, secret := range []string{"none", "head", "tail"} {
			input := strings.Repeat("ordinary text ", size/14)
			if secret == "head" {
				input = "Bearer exampleTOKEN123 " + input
			}
			if secret == "tail" {
				input += " Bearer exampleTOKEN123"
			}
			for _, old := range []bool{true, false} {
				b.Run(fmt.Sprintf("%d/%s/old=%t", size, secret, old), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if old {
							originalRedactPreview(input, 96)
						} else {
							RedactPreview(input, 96)
						}
					}
				})
			}
		}
	}
}

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
