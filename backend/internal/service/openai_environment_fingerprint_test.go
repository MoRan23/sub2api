package service

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestSelectOpenAIEnvironmentFingerprintAndFallback(t *testing.T) {
	original := openAIEnvironmentFingerprintRandomInt
	defer func() { openAIEnvironmentFingerprintRandomInt = original }()

	for i, want := range openAIEnvironmentFingerprints {
		openAIEnvironmentFingerprintRandomInt = func(_ *big.Int) (*big.Int, error) {
			return big.NewInt(int64(i)), nil
		}
		if got := SelectOpenAIEnvironmentFingerprint(); got != want {
			t.Fatalf("selected fingerprint = %q, want %q", got, want)
		}
	}

	for _, result := range []*big.Int{nil, big.NewInt(-1), big.NewInt(int64(len(openAIEnvironmentFingerprints)))} {
		openAIEnvironmentFingerprintRandomInt = func(_ *big.Int) (*big.Int, error) {
			return result, errors.New("entropy unavailable")
		}
		if got := SelectOpenAIEnvironmentFingerprint(); got != openAIEnvironmentFingerprints[0] {
			t.Fatalf("fallback fingerprint = %q, want %q", got, openAIEnvironmentFingerprints[0])
		}
	}
}

func TestNormalizeOpenAIEnvironmentFingerprint(t *testing.T) {
	valid := "(Ubuntu 22.4.0; x86_64) xterm-256color"
	if got, err := NormalizeOpenAIEnvironmentFingerprint("  " + valid + "  "); err != nil || got != valid {
		t.Fatalf("normalize valid fingerprint = %q, %v", got, err)
	}
	for _, invalid := range []string{"", "line one\nline two", strings.Repeat("x", openAIEnvironmentFingerprintMaxLen+1), "终端"} {
		if _, err := NormalizeOpenAIEnvironmentFingerprint(invalid); err == nil {
			t.Fatalf("fingerprint %q should be rejected", invalid)
		}
	}
}

func TestBuildOpenAIUserAgentWithEnvironmentPreservesClientFamily(t *testing.T) {
	SetCodexCanonicalUserAgentResolver(func() string {
		return "codex_vscode/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"
	})
	defer SetCodexCanonicalUserAgentResolver(nil)

	const fingerprint = "(Mac OS X 15.1.0; arm64) iTerm.app"
	ua, err := BuildOpenAIUserAgentWithEnvironment(
		"codex_vscode/0.120.0 (Windows 10.0.19045; x86_64) WindowsTerminal",
		fingerprint,
	)
	if err != nil {
		t.Fatalf("build user agent: %v", err)
	}
	if ua != "codex_vscode/0.146.0 "+fingerprint {
		t.Fatalf("user agent = %q", ua)
	}
	if got := OpenAIEnvironmentFingerprintFromUserAgent(ua); got != fingerprint {
		t.Fatalf("extracted fingerprint = %q, want %q", got, fingerprint)
	}

	fallback, err := BuildOpenAIUserAgentWithEnvironment("curl/8.0", fingerprint)
	if err != nil {
		t.Fatalf("build fallback user agent: %v", err)
	}
	if fallback != "codex-tui/0.146.0 "+fingerprint {
		t.Fatalf("fallback user agent = %q", fallback)
	}
}

func TestOpenAIOutboundUserAgentUsesCurrentVersionAndStoredFingerprint(t *testing.T) {
	SetCodexCanonicalUserAgentResolver(func() string {
		return "codex-tui/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color"
	})
	defer SetCodexCanonicalUserAgentResolver(nil)

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"user_agent": "codex_cli_rs/0.120.0 (Windows 11.0.26100; x86_64) WindowsTerminal",
		},
	}
	if got, want := account.GetOpenAIOutboundUserAgent(), "codex_cli_rs/0.146.0 (Windows 11.0.26100; x86_64) WindowsTerminal"; got != want {
		t.Fatalf("outbound user agent = %q, want %q", got, want)
	}
}

func TestPrepareOpenAIAccountUserAgentForCreate(t *testing.T) {
	original := openAIEnvironmentFingerprintRandomInt
	defer func() { openAIEnvironmentFingerprintRandomInt = original }()
	openAIEnvironmentFingerprintRandomInt = func(_ *big.Int) (*big.Int, error) {
		return big.NewInt(1), nil
	}

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":    "secret",
			"user_agent": "codex_cli_rs/0.120.0 (Old OS; x86_64) old-terminal",
		},
	}
	PrepareOpenAIAccountUserAgentForCreate(account)
	if got := account.GetOpenAIEnvironmentFingerprint(); got != openAIEnvironmentFingerprints[1] {
		t.Fatalf("created fingerprint = %q, want %q", got, openAIEnvironmentFingerprints[1])
	}
	if !strings.HasPrefix(account.GetOpenAIUserAgent(), "codex_cli_rs/") {
		t.Fatalf("client family was not preserved: %q", account.GetOpenAIUserAgent())
	}

	parentID := int64(1)
	shadow := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	PrepareOpenAIAccountUserAgentForCreate(shadow)
	if shadow.GetOpenAIUserAgent() != "" {
		t.Fatalf("shadow received independent user agent: %q", shadow.GetOpenAIUserAgent())
	}
}

func TestResolveOpenAIAccountUserAgentUsesShadowParent(t *testing.T) {
	parentID := int64(9)
	parent := &Account{
		ID:       parentID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"user_agent": "codex_cli_rs/0.120.0 (Windows 11.0.26100; x86_64) WindowsTerminal",
		},
	}
	shadow := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo := &openAIEnvironmentAdminRepoStub{account: parent}

	got, err := resolveOpenAIAccountUserAgent(context.Background(), repo, shadow)
	if err != nil {
		t.Fatalf("resolve shadow user agent: %v", err)
	}
	if !strings.HasPrefix(got, "codex_cli_rs/") || !strings.HasSuffix(got, "(Windows 11.0.26100; x86_64) WindowsTerminal") {
		t.Fatalf("shadow user agent = %q", got)
	}
}

type openAIEnvironmentAdminRepoStub struct {
	AccountRepository
	account *Account
}

func (r *openAIEnvironmentAdminRepoStub) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *openAIEnvironmentAdminRepoStub) Update(_ context.Context, account *Account) error {
	r.account = account
	return nil
}

func TestUpdateAccountEditsOpenAIEnvironmentFingerprint(t *testing.T) {
	const oldUA = "codex_cli_rs/0.120.0 (Ubuntu 22.4.0; x86_64) xterm-256color"
	fingerprint := "(Windows 11.0.26100; x86_64) WindowsTerminal"
	repo := &openAIEnvironmentAdminRepoStub{account: &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Status:   StatusActive,
		Credentials: map[string]any{
			"api_key":    "secret",
			"user_agent": oldUA,
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{
		OpenAIEnvironmentFingerprint: &fingerprint,
		Credentials: map[string]any{
			"user_agent": "codex_vscode/0.1.0 (ignored; x86_64) ignored",
		},
	})
	if err != nil {
		t.Fatalf("update fingerprint: %v", err)
	}
	if updated.GetOpenAIEnvironmentFingerprint() != fingerprint {
		t.Fatalf("updated fingerprint = %q", updated.GetOpenAIEnvironmentFingerprint())
	}
	if !strings.HasPrefix(updated.GetOpenAIUserAgent(), "codex_cli_rs/") {
		t.Fatalf("dedicated field did not preserve current family: %q", updated.GetOpenAIUserAgent())
	}
	if updated.GetCredential("api_key") != "secret" {
		t.Fatal("sensitive API key was not preserved")
	}
}

func TestUpdateAccountRejectsUnsupportedEnvironmentFingerprint(t *testing.T) {
	fingerprint := "(Ubuntu 22.4.0; x86_64) xterm-256color"
	parentID := int64(1)
	for _, account := range []*Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeSetupToken},
	} {
		repo := &openAIEnvironmentAdminRepoStub{account: account}
		_, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{
			OpenAIEnvironmentFingerprint: &fingerprint,
		})
		if err == nil || infraerrors.Reason(err) != "OPENAI_ENVIRONMENT_FINGERPRINT_UNSUPPORTED" {
			t.Fatalf("account %+v error = %v", account, err)
		}
	}
}
