//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

func BenchmarkHeaderOverrideRequestIdentity(b *testing.B) {
	maximumTemplates := make([]string, maxHeaderOverrideEntries)
	for index := range maximumTemplates {
		maximumTemplates[index] = fmt.Sprintf(`"x-template-%02d":"u={{user_id}};k={{api_key_id}};g={{group_id}};r={{request_id}}"`, index)
	}
	for _, fixture := range []struct {
		name        string
		credentials string
	}{
		{"disabled", `{}`},
		{"static", `{"header_override_enabled":true,"header_overrides":{"x-relay":"relay-secret","x-team":"team-a"}}`},
		{"templates", `{"header_override_enabled":true,"header_overrides":{"x-relay":"relay-secret-{{user_id}}","x-trace":"u={{user_id}};k={{api_key_id}};g={{group_id}};r={{request_id}}"}}`},
		{"templates_64", `{"header_override_enabled":true,"header_overrides":{` + strings.Join(maximumTemplates, ",") + `}}`},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			if err := json.Unmarshal([]byte(fixture.credentials), &account.Credentials); err != nil {
				b.Fatal(err)
			}
			account.GetHeaderOverrides()
			ctx := context.WithValue(context.Background(), ctxkey.RequestID, "benchmark-request")
			groupID := int64(9)
			b.ReportAllocs()
			for b.Loop() {
				requestCtx := WithHeaderOverrideRequestScope(ctx, 7, 31, &groupID)
				headers := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer fixture"}}
				account.ApplyHeaderOverrides(requestCtx, headers)
				finalizeHeaderOverrideReportedHeaders(requestCtx, account.ID, headers)
			}
		})
	}
}

func BenchmarkHeaderOverrideWSCompatibility(b *testing.B) {
	for _, fixture := range []struct {
		name   string
		prefix string
	}{
		{"no_template", ""},
		{"template", "relay-"},
		{"template_4KiB_literal", strings.Repeat("x", 4096)},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			groupID := int64(9)
			ctx := withHeaderOverrideConnectionScope(WithHeaderOverrideRequestScope(context.Background(), 7, 31, &groupID))
			headers := http.Header{"Authorization": {"Bearer fixture"}}
			if fixture.prefix != "" {
				credentials := `{"header_override_enabled":true,"header_overrides":{"x-relay":"` + fixture.prefix + `{{user_id}}","x-end-user":"{{api_key_id}}"}}`
				if err := json.Unmarshal([]byte(credentials), &account.Credentials); err != nil {
					b.Fatal(err)
				}
				account.ApplyHeaderOverrides(ctx, headers)
			}
			req := openAIWSAcquireRequest{
				Account:         account,
				WSURL:           "wss://example.invalid/v1/responses",
				Headers:         headers,
				ReportedHeaders: finalizeHeaderOverrideReportedHeaders(ctx, account.ID, headers),
			}
			b.ReportAllocs()
			for b.Loop() {
				openAIWSAcquireCompatibility(req)
			}
		})
	}
}
