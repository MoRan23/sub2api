package responseheaders

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestImagegenDiagnosticHeaderDefaultAndExplicitRemoval(t *testing.T) {
	src := http.Header{"X-Request-Id": {"req-outer"}, "X-Codex-Imagegen-Request-Id": {"req-inner"}, "X-Openai-Authorization-Error": {"private"}, "X-Error-Json": {"encoded-private"}}
	filtered := FilterHeaders(src, nil)
	if filtered.Get("x-request-id") != "req-outer" || filtered.Get("x-codex-imagegen-request-id") != "req-inner" {
		t.Fatal("both request IDs must remain independent")
	}
	if filtered.Get("x-openai-authorization-error") != "" || filtered.Get("x-error-json") != "" {
		t.Fatal("administrator diagnostics cannot be newly forwarded")
	}
	filtered = FilterHeaders(src, CompileHeaderFilter(config.ResponseHeaderConfig{Enabled: true, ForceRemove: []string{"x-codex-imagegen-request-id"}}))
	if filtered.Get("x-codex-imagegen-request-id") != "" {
		t.Fatal("explicit removal wins")
	}
}

func TestFilterHeadersDisabledUsesDefaultAllowlist(t *testing.T) {
	src := http.Header{}
	src.Add("Content-Type", "application/json")
	src.Add("X-Request-Id", "req-123")
	src.Add("X-Test", "ok")
	src.Add("Connection", "keep-alive")
	src.Add("Content-Length", "123")

	cfg := config.ResponseHeaderConfig{
		Enabled:     false,
		ForceRemove: []string{"x-request-id"},
	}

	filtered := FilterHeaders(src, CompileHeaderFilter(cfg))
	if filtered.Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type passthrough, got %q", filtered.Get("Content-Type"))
	}
	if filtered.Get("X-Request-Id") != "req-123" {
		t.Fatalf("expected X-Request-Id allowed, got %q", filtered.Get("X-Request-Id"))
	}
	if filtered.Get("X-Test") != "" {
		t.Fatalf("expected X-Test removed, got %q", filtered.Get("X-Test"))
	}
	if filtered.Get("Connection") != "" {
		t.Fatalf("expected Connection to be removed, got %q", filtered.Get("Connection"))
	}
	if filtered.Get("Content-Length") != "" {
		t.Fatalf("expected Content-Length to be removed, got %q", filtered.Get("Content-Length"))
	}
}

func TestFilterHeadersAllowsReasoningIncludedByDefault(t *testing.T) {
	src := http.Header{}
	src.Set("X-Reasoning-Included", "1")

	filtered := FilterHeaders(src, CompileHeaderFilter(config.ResponseHeaderConfig{}))
	if got := filtered.Get("X-Reasoning-Included"); got != "1" {
		t.Fatalf("expected X-Reasoning-Included passthrough, got %q", got)
	}
}

func TestFilterHeadersForceRemoveOverridesReasoningIncluded(t *testing.T) {
	src := http.Header{}
	src.Set("X-Reasoning-Included", "1")

	filtered := FilterHeaders(src, CompileHeaderFilter(config.ResponseHeaderConfig{
		Enabled:     true,
		ForceRemove: []string{"x-reasoning-included"},
	}))
	if got := filtered.Get("X-Reasoning-Included"); got != "" {
		t.Fatalf("expected X-Reasoning-Included removal, got %q", got)
	}
}

func TestFilterHeadersEnabledUsesAllowlist(t *testing.T) {
	src := http.Header{}
	src.Add("Content-Type", "application/json")
	src.Add("X-Extra", "ok")
	src.Add("X-Remove", "nope")
	src.Add("X-Blocked", "nope")

	cfg := config.ResponseHeaderConfig{
		Enabled:           true,
		AdditionalAllowed: []string{"x-extra"},
		ForceRemove:       []string{"x-remove"},
	}

	filtered := FilterHeaders(src, CompileHeaderFilter(cfg))
	if filtered.Get("Content-Type") != "application/json" {
		t.Fatalf("expected Content-Type allowed, got %q", filtered.Get("Content-Type"))
	}
	if filtered.Get("X-Extra") != "ok" {
		t.Fatalf("expected X-Extra allowed, got %q", filtered.Get("X-Extra"))
	}
	if filtered.Get("X-Remove") != "" {
		t.Fatalf("expected X-Remove removed, got %q", filtered.Get("X-Remove"))
	}
	if filtered.Get("X-Blocked") != "" {
		t.Fatalf("expected X-Blocked removed, got %q", filtered.Get("X-Blocked"))
	}
}
