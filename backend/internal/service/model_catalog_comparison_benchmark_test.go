//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
)

var catalogBenchmarkOwnership CompositeModelOwnership
var catalogBenchmarkResponse *OpenAIModelsResponse

// baseline reproduces e6de1c8eb's per-model scan after the repository load;
// indexed additionally includes the bounded store lookup (no database needed).
func BenchmarkCompositeOwnershipComparison(b *testing.B) {
	accounts := make([]Account, 2000)
	for i := range accounts {
		mapping := make(map[string]any, 16)
		for j := 0; j < 16; j++ {
			mapping[fmt.Sprintf("model-%d", j)] = "target"
		}
		accounts[i] = Account{ID: int64(i + 1), Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": mapping}}
	}
	index, size := buildCompositeOwnershipIndex(accounts)
	store := NewModelCatalogStore(64<<20, 512)
	store.Put("ownership:1", index, size, store.Revision())
	b.Run("baseline_scan_2000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			platforms := make(map[string]struct{})
			ownership := CompositeModelOwnership{}
			for _, a := range accounts {
				if explicitModelMappingClaims(a, "model-0") {
					platforms[a.Platform] = struct{}{}
				}
			}
			if len(platforms) == 1 {
				for platform := range platforms {
					ownership.TargetPlatform = platform
				}
				ownership.Matched = true
			}
			catalogBenchmarkOwnership = ownership
		}
	})
	b.Run("bounded_index_2000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			value, _ := store.Get("ownership:1")
			catalogBenchmarkOwnership = value.(compositeOwnershipIndex)["model-0"]
		}
	})
}

func BenchmarkGroupCatalogRepresentationComparison(b *testing.B) {
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	accounts := make([]Account, 2000)
	for i := range accounts {
		accounts[i] = groupCatalogAccount(int64(i+1), map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"})
		accounts[i].PrepareSharedSnapshot()
	}
	source := catalogFixture(`{"models":[{"slug":"gpt-5.6-sol","display_name":"Official","context_window":272000,"shell_type":"shell_command","unknown":{"keep":true}}]}`)
	source.ETag = codexModelsManifestBodyETag(source.Body)
	svc := &OpenAIGatewayService{}
	ctx := context.Background()
	warm, _, err := svc.cachedOpenAIGroupModelsCatalog(ctx, group, accounts, accounts, nil, source, source, "", "client", "projection-version", true, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("baseline_rebuild", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			result, _, err := buildOpenAIGroupModelsCatalog(group, accounts, accounts, nil, source, source, warm.ETag, true, nil)
			if err != nil {
				b.Fatal(err)
			}
			catalogBenchmarkResponse = result
		}
	})
	b.Run("cached_304", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			result, _, err := svc.cachedOpenAIGroupModelsCatalog(ctx, group, accounts, accounts, nil, source, source, warm.ETag, "client", "projection-version", true, nil)
			if err != nil {
				b.Fatal(err)
			}
			catalogBenchmarkResponse = result
		}
	})
}
