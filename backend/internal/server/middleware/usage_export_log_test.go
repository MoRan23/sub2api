package middleware

import (
	"strings"
	"testing"
)

func TestUsageExportCapabilityPathRedacted(t *testing.T) {
	ticket := strings.Repeat("a", 48)
	got := redactCapabilityPath("/api/v1/usage-exports/download/" + ticket)
	if strings.Contains(got, ticket) || got != "/api/v1/usage-exports/download/[redacted]" {
		t.Fatalf("unsafe capability path %q", got)
	}
	if got = redactCapabilityPath("/api/v1/admin/usage/exports"); got != "/api/v1/admin/usage/exports" {
		t.Fatal(got)
	}
}
