package securityaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func BenchmarkPromptCombinationPlainPathScan(b *testing.B) {
	for _, size := range []int{64 << 10, 8 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			text := strings.Repeat("ordinary discussion text ", size/25)
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for b.Loop() {
				spans := combinationTextSpans(text)
				if spans != nil {
					b.Fatal("plain text unexpectedly requires filtered spans")
				}
				runtime.KeepAlive(spans)
			}
		})
	}
}

func BenchmarkPromptCombinationEscapedURLSuffix(b *testing.B) {
	for _, size := range []int{64 << 10, 8 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			text := "https://reader%40name@example.invalid/" + strings.Repeat("%61", size/3) + "?tag=%62"
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for b.Loop() {
				spans := combinationTextSpans(text)
				if spans == nil {
					b.Fatal("URL should isolate its hostname")
				}
				runtime.KeepAlive(spans)
			}
		})
	}
}

func BenchmarkPromptCombinationWhitelistLookup(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		keys := make([]KeywordWhitelistKey, count)
		for index := range keys {
			keys[index] = KeywordWhitelistKey{UserID: int64(index + 1), APIKeyID: int64(index + 1001)}
		}
		policy := ActiveCombinationPolicy{whitelistKeys: compileCombinationWhitelist(keys)}
		for _, hit := range []bool{false, true} {
			b.Run(fmt.Sprintf("keys=%d/hit=%t", count, hit), func(b *testing.B) {
				key := keys[count-1]
				if !hit {
					key.UserID++
				}
				b.ReportAllocs()
				for b.Loop() {
					if policy.whitelistsKey(key.UserID, key.APIKeyID) != hit {
						b.Fatal("whitelist result changed")
					}
				}
			})
		}
	}
}

// Both exemptions run before JSON preparation. The benchmark makes accidental
// parsing or retention of the large original body visible in allocations.
func BenchmarkPromptCombinationWhitelistBypass(b *testing.B) {
	for _, size := range []int{64 << 10, 8 << 20} {
		body, err := json.Marshal(map[string]any{"input": strings.Repeat("ordinary text ", size/14)})
		if err != nil {
			b.Fatal(err)
		}
		for _, scope := range []string{"global", "policy"} {
			for _, count := range []int{1, 1000} {
				b.Run(fmt.Sprintf("%s/keys=%d/%d", scope, count, size), func(b *testing.B) {
					keys := make([]KeywordWhitelistKey, count)
					for index := range keys {
						keys[index] = KeywordWhitelistKey{UserID: int64(index + 1), APIKeyID: int64(index + 1001)}
					}
					key := keys[count-1]
					cfg := storageConfig{KeywordAllGroups: true, AllGroups: true}
					if scope == "global" {
						cfg.KeywordWhitelistKeys = keys
					}
					for index := range 8 {
						policy := CombinationPolicy{ID: fmt.Sprintf("policy%d", index), Name: "synthetic policy", Enabled: true, TopicRules: []string{"orchid"}, IntentRules: []string{"prune"}}
						if scope == "policy" {
							policy.KeywordWhitelistKeys = keys
						}
						cfg.CombinationPolicies = append(cfg.CombinationPolicies, policy)
					}
					active, err := ActiveFromStorage(cfg, true, prefixEncryptor{})
					if err != nil {
						b.Fatal(err)
					}
					manager := &ConfigManager{}
					manager.snapshot.Store(&activeConfigSnapshot{active: active, storage: cfg})
					manager.expectedBlocking.Store(true)
					service := &PromptService{config: manager}
					req := Request{Protocol: "responses", Body: body, UserID: key.UserID, APIKeyID: key.APIKeyID}
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						decision, err := service.CheckKeyword(context.Background(), req)
						if err != nil || !decision.AllowNextStage {
							b.Fatalf("whitelist bypass failed: %+v, %v", decision, err)
						}
					}
				})
			}
		}
	}
}
