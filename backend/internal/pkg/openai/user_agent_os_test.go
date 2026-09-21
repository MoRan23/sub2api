package openai

import "testing"

func TestDetectOSFamilyFromUserAgent(t *testing.T) {
	for _, test := range []struct {
		name, ua, want string
	}{
		{"codex windows", "codex-tui/0.152.0 (Windows 10.0.26200; x86_64) WindowsTerminal (codex-tui; 0.152.0)", "windows"},
		{"codex ubuntu", "codex-tui/0.152.0 (Ubuntu 24.04.4; x86_64) xterm-256color", "linux"},
		{"existing telemetry debian", "codex-tui/0.152.0 (Debian 13; x86_64) xterm", "linux"},
		{"existing telemetry arch linux", "codex-tui/0.152.0 (Arch Linux; x86_64) xterm", "linux"},
		{"unrecognized distribution without linux hint", "codex-tui/0.152.0 (Fedora 43; x86_64) xterm", ""},
		{"terminal is not OS", "codex-tui/0.152.0 (Ubuntu 24.04.4; x86_64) WindowsTerminal", "linux"},
		{"suffix is not OS", "codex-tui/0.152.0 (Mac OS 26.6.2; arm64) LinuxTerminal (Windows; 1.0)", "macos"},
		{"mac os x", "codex_cli_rs/0.152.0 (Mac OS X 15.1; arm64) iTerm.app", "macos"},
		{"macos", "Codex/1.0 (macOS 26.6; arm64) Terminal", "macos"},
		{"darwin", "Codex/1.0 (Darwin 25.0; arm64) Terminal", "macos"},
		{"browser windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0", "windows"},
		{"browser macintosh", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15", "macos"},
		{"browser linux", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0", "linux"},
		{"android is not desktop linux", "Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 Chrome/140.0 Mobile Safari/537.36", ""},
		{"android tablet is not desktop linux", "Mozilla/5.0 (Linux; Android 15; Tablet) AppleWebKit/537.36 Chrome/140.0 Safari/537.36", ""},
		{"iphone is not macos", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148", ""},
		{"ipad is not macos", "Mozilla/5.0 (iPad; CPU OS 18_5 like Mac OS X) AppleWebKit/605.1.15", ""},
		{"ipod is not macos", "Mozilla/5.0 (iPod touch; CPU iPhone OS 15_8 like Mac OS X) AppleWebKit/605.1.15", ""},
		{"ipad desktop mode remains mobile", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1", ""},
		{"windows phone is not desktop windows", "Mozilla/5.0 (Windows Phone 10.0; Android 6.0; Microsoft; Lumia) AppleWebKit/537.36 IEMobile/11.0", ""},
		{"windows phone without android", "Mozilla/5.0 (Windows Phone OS 7.5; Trident/5.0; IEMobile/9.0)", ""},
		{"bare ios", "iOS like Mac OS X", ""},
		{"bare ipados", "iPadOS Darwin", ""},
		{"mobile word boundary", "codex-tui/0.152.0 (Linux; x86_64) MobileTerminal", "linux"},
		{"same family aliases", "Codex/1.0 (Ubuntu Linux; x86_64) xterm", "linux"},
		{"case insensitive bare OS", "  WINDOWS 11  ", "windows"},
		{"bare mac OS", "Mac OS", "macos"},
		{"bare linux", "Linux different-version", "linux"},
		{"conflicting OS field", "codex-tui/0.152.0 (Windows Linux; x86_64) WindowsTerminal", ""},
		{"conflicting bare hints", "Darwin Ubuntu", ""},
		{"unknown OS ignores terminal", "codex-tui/0.152.0 (Unknown OS; x86_64) WindowsTerminal", ""},
		{"unknown OS ignores later group", "codex-tui/0.152.0 (Unknown OS; x86_64) xterm (Windows; 1.0)", ""},
		{"word boundary", "NotWindows LinuxTerminal Darwinian macOSClient", ""},
		{"missing close", "codex-tui/0.152.0 (Windows NT 10.0", ""},
		{"nested group", "codex-tui/0.152.0 ((Linux); x86_64)", ""},
		{"unexpected close", "Windows)", ""},
		{"invalid header", "codex-tui/0.152.0 (Linux; x86_64)\r\nWindows", ""},
		{"missing environment", "codex-tui/0.152.0", ""},
		{"empty", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := DetectOSFamilyFromUserAgent(test.ua); got != test.want {
				t.Fatalf("DetectOSFamilyFromUserAgent(%q) = %q, want %q", test.ua, got, test.want)
			}
		})
	}
}
