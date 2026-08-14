package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexUserAgentVersion(t *testing.T) {
	require.Equal(t, "0.146.0", CodexUserAgentVersion("codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"))
	require.Equal(t, "0.146.0", CodexUserAgentVersion("  codex_cli_rs/0.146.0  "))
	// 预发布后缀原样保留：出站 version 头必须与 UA 版本段逐字一致。
	require.Equal(t, "0.147.0-alpha.4", CodexUserAgentVersion("codex-tui/0.147.0-alpha.4 (Mac OS X 14.0; arm64) iTerm"))
	// 非 `{client}/{version}` 形态取不到版本段。
	require.Empty(t, CodexUserAgentVersion("curl 8.7.1"))
	require.Empty(t, CodexUserAgentVersion("/0.146.0"))
	require.Empty(t, CodexUserAgentVersion(""))
}

// 管理员在面板 / 账号上配置的 UA 填写于某个历史版本，逐字沿用会把出站身份钉死在陈旧版本上。
// 重建只动版本声明，OS / 架构 / 终端指纹必须原样保留——那是该配置项唯一不可替代的价值。
func TestSetCodexUserAgentVersion(t *testing.T) {
	require.Equal(t,
		"codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
		SetCodexUserAgentVersion("codex_cli_rs/0.125.0 (Ubuntu 22.4.0; x86_64) xterm-256color", "0.146.0"),
	)
	require.Equal(t, "codex_cli_rs/0.146.0", SetCodexUserAgentVersion("codex_cli_rs/0.1.0", "0.146.0"))

	// 尾部官方客户端标识组与首段是同一个版本声明的两个出口，必须一并更新，
	// 否则拼出首段声明新版本、尾部仍是旧版本的自相矛盾身份。
	require.Equal(t,
		"cccc/0.146.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.146.0)",
		SetCodexUserAgentVersion("cccc/0.142.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.142.0)", "0.146.0"),
	)
	// OS 括号组不是客户端标识，不得被误改。
	require.Equal(t,
		"codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64)",
		SetCodexUserAgentVersion("codex_cli_rs/0.125.0 (Ubuntu 22.4.0; x86_64)", "0.146.0"),
	)

	// 无法重建时返回空串，由调用方决定整体回退，绝不拼出畸形身份。
	require.Empty(t, SetCodexUserAgentVersion("not-a-codex-client", "0.146.0"))
	require.Empty(t, SetCodexUserAgentVersion("codex_cli_rs/", "0.146.0"))
	require.Empty(t, SetCodexUserAgentVersion("/0.1.0", "0.146.0"))
	require.Empty(t, SetCodexUserAgentVersion("codex_cli_rs/0.1.0", ""))
}

func TestEnsureCodexTUIUserAgent(t *testing.T) {
	const version = "0.147.0"
	const canonical = "codex-tui/0.147.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.147.0)"
	tests := []struct {
		name string
		ua   string
		want string
	}{
		{
			name: "missing trailer",
			ua:   "codex-tui/0.140.0 (Ubuntu 22.4.0; x86_64) xterm-256color",
			want: canonical,
		},
		{
			name: "old trailer and non-TUI family",
			ua:   "codex_vscode/0.140.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex_vscode; 0.140.0)",
			want: canonical,
		},
		{
			name: "duplicate trailers",
			ua:   "codex-tui/0.140.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex_vscode; 0.139.0) (codex-tui; broken)",
			want: canonical,
		},
		{
			name: "leading and trailer versions disagree",
			ua:   "codex-tui/0.120.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.140.0)",
			want: canonical,
		},
		{
			name: "macOS environment",
			ua:   "codex_cli_rs/0.140.0 (Mac OS X 15.1.0; arm64) iTerm.app",
			want: "codex-tui/0.147.0 (Mac OS X 15.1.0; arm64) iTerm.app (codex-tui; 0.147.0)",
		},
		{
			name: "Windows environment",
			ua:   "codex-tui/0.140.0 (Windows 11.0.26100; x86_64) WindowsTerminal",
			want: "codex-tui/0.147.0 (Windows 11.0.26100; x86_64) WindowsTerminal (codex-tui; 0.147.0)",
		},
		{name: "third-party UA", ua: "curl/8.0 (Ubuntu 22.4.0; x86_64) xterm-256color"},
		{name: "third-party trailer", ua: "codex-tui/0.140.0 (Ubuntu 22.4.0; x86_64) xterm-256color (other-client; 0.140.0)"},
		{name: "missing environment", ua: "codex-tui/0.140.0"},
		{name: "missing terminal", ua: "codex-tui/0.140.0 (Ubuntu 22.4.0; x86_64)"},
		{name: "malformed trailer", ua: "codex-tui/0.140.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.140.0"},
		{name: "control character", ua: "codex-tui/0.140.0 (Ubuntu 22.4.0; x86_64) xterm\nother"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := EnsureCodexTUIUserAgent(tt.ua, version)
			require.Equal(t, tt.want != "", ok)
			require.Equal(t, tt.want, got)
		})
	}

	got, ok := EnsureCodexTUIUserAgent(canonical, version)
	require.True(t, ok)
	require.Equal(t, canonical, got)
	got, ok = EnsureCodexTUIUserAgent("(Ubuntu 22.4.0; x86_64) xterm-256color", version)
	require.True(t, ok)
	require.Equal(t, canonical, got)
	got, ok = EnsureCodexTUIUserAgent(canonical, "0.148.0-alpha.4")
	require.True(t, ok)
	require.Equal(t, "codex-tui/0.148.0-alpha.4 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.148.0-alpha.4)", got)
	for _, invalidVersion := range []string{"bogus", "0.147.0\r\nX-Injected: 1", "v0.147.0", ""} {
		got, ok = EnsureCodexTUIUserAgent(canonical, invalidVersion)
		require.False(t, ok, "version %q", invalidVersion)
		require.Empty(t, got)
	}
}
