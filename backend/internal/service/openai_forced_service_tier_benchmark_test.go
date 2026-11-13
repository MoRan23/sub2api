package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

var benchmarkServiceTierSupported bool
var benchmarkServiceTierResponse *OpenAIModelsResponse
var benchmarkServiceTierBody []byte

func BenchmarkOpenAIServiceTierCapabilityIndex(b *testing.B) {
	catalog := parseOpenAIServiceTierCatalog([]byte(`{"models":[{"slug":"model","service_tiers":[{"id":"ultrafast"}]}]}`))
	b.ReportAllocs()
	for b.Loop() {
		benchmarkServiceTierSupported = catalog.ultrafast["model"]
	}
}

func BenchmarkOpenAIServiceTierCachedLargeCatalog(b *testing.B) {
	ctx := context.Background()
	svc := &OpenAIGatewayService{}
	request := openAIModelsRequest{accountID: 1, credentialAccountID: 1, url: "https://upstream.example/v1/models", headers: make(http.Header), capabilityOnly: true}
	body := []byte(`{"models":[{"slug":"model","service_tiers":[{"id":"ultrafast"}],"instructions":"` + strings.Repeat("x", 400<<10) + `"}]}`)
	manifest := &OpenAIModelsResponse{Body: body, upstreamSourceBody: body, serviceTierCatalog: parseOpenAIServiceTierCatalog(body), observedAt: time.Now()}
	svc.openAIModelsCache.set(buildOpenAIModelsCacheKey(request), manifest, time.Now())
	fetch := func(context.Context, string) (*OpenAIModelsResponse, error) {
		b.Fatal("unexpected catalog refresh")
		return nil, nil
	}
	b.ReportAllocs()
	for b.Loop() {
		result, err := svc.fetchCachedOpenAIModels(ctx, request, fetch, "")
		if err != nil || len(result.Body) != 0 || result.serviceTierCatalog != manifest.serviceTierCatalog {
			b.Fatal("cache hit did not reuse the index")
		}
		benchmarkServiceTierResponse = result
	}
}

func BenchmarkOpenAIServiceTierUnchangedBody(b *testing.B) {
	ctx := context.Background()
	svc := &OpenAIGatewayService{}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{ForceOpenAIFastExtraKey: true}}
	body := []byte(`{"model":"model","service_tier":"priority","input":"` + strings.Repeat("x", 2<<20) + `"}`)
	b.ReportAllocs()
	for b.Loop() {
		out, err := svc.applyOpenAIFastPolicyToBody(ctx, account, "model", body)
		if err != nil || &out[0] != &body[0] {
			b.Fatal("unchanged tier copied the body")
		}
		benchmarkServiceTierBody = out
	}
}
