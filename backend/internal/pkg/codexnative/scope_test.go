package codexnative

import (
	"context"
	"testing"
)

func TestResolvePlatformPrecedence(t *testing.T) {
	for _, test := range []struct {
		name, final, source, account, canonical string
		platform                                Platform
		matchedBy                               string
	}{
		{"final", "codex-tui/0.154.0 (Windows 10.0.26200; x86_64)", "Ubuntu", "Mac OS", "Linux", Windows, "final_user_agent"},
		{"mac-final", "codex-tui/0.200.0 (Mac OS 30; arm64)", "Windows", "Ubuntu", "Linux", MacOS, "final_user_agent"},
		{"linux-final", "codex-tui/0.154.0 (Ubuntu 24.04.4; x86_64)", "", "", "", Linux, "final_user_agent"},
		{"terminal-does-not-override-os", "codex-tui/0.154.0 (Ubuntu 24.04.4; x86_64) WindowsTerminal", "Windows", "", "", Linux, "final_user_agent"},
		{"conflicting-final-falls-back", "codex-tui/0.154.0 (Windows Linux; x86_64) WindowsTerminal", "Mac OS", "Windows", "Linux", MacOS, "source_user_agent"},
		{"unknown-final-ignores-terminal", "codex-tui/0.154.0 (Unknown; x86_64) WindowsTerminal", "", "Mac OS", "Linux", MacOS, "account_user_agent"},
		{"otel-source", "OTel-OTLP-Exporter-Rust/0.27.0", "codex-tui (Macintosh; arm64)", "Windows", "Linux", MacOS, "source_user_agent"},
		{"account", "", "unknown", "Codex (Darwin; arm64)", "Windows", MacOS, "account_user_agent"},
		{"canonical", "", "", "", "codex (Windows)", Windows, "canonical_user_agent"},
		{"builtin", "unknown-agent", "", "", "", Linux, "builtin_linux"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selection := Resolve(test.final, Scope{SourceUserAgent: test.source, AccountUserAgent: test.account, CanonicalUserAgent: test.canonical})
			if selection.Platform != test.platform || selection.MatchedBy != test.matchedBy {
				t.Fatalf("selection = %+v", selection)
			}
			if len(selection.Digest) != 64 || selection.ProfileID != ProfileVersion+"/"+string(test.platform) {
				t.Fatalf("invalid profile identity: %+v", selection)
			}
		})
	}
}

func TestScopeFrozenAndExplicitlyDisabled(t *testing.T) {
	original := Scope{AccountID: 13, AccountUserAgent: "Windows", Purpose: "auth"}
	ctx := WithScope(context.Background(), original)
	original.AccountUserAgent = "Linux"
	got, ok := ScopeFromContext(ctx)
	if !ok || got.AccountUserAgent != "Windows" {
		t.Fatalf("scope was not frozen: %+v", got)
	}
	if _, ok := ScopeFromContext(WithoutScope(ctx)); ok {
		t.Fatal("non-OAuth retry retained inherited scope")
	}
	if _, ok := ScopeFromContext(nil); ok {
		t.Fatal("nil context has scope")
	}
}

func TestProfileDigestAndSpecIsolation(t *testing.T) {
	windows := Resolve("Windows", Scope{})
	if Resolve("Windows different-version", Scope{AccountID: 1}).Digest != windows.Digest {
		t.Fatal("profile digest depends on UA version or request/account")
	}
	if Resolve("Linux", Scope{}).Digest == windows.Digest || Resolve("Mac OS", Scope{}).Digest == windows.Digest {
		t.Fatal("platform profiles are not isolated")
	}
	first, _ := profileSpec(Linux)
	second, _ := profileSpec(Linux)
	first.CipherSuites[0] = 0
	first.CompressionMethods[0] = 1
	if second.CipherSuites[0] != 4866 || second.CompressionMethods[0] != 0 || first.Extensions[0] == second.Extensions[0] {
		t.Fatal("handshakes share mutable profile state")
	}
}
