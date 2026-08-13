package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/big"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const openAIEnvironmentFingerprintMaxLen = 256

// openAIEnvironmentFingerprints is the built-in pool used for newly created
// OpenAI accounts. The selected value is persisted in credentials.user_agent
// and remains stable until an administrator edits the account.
var openAIEnvironmentFingerprints = []string{
	"(Ubuntu 22.4.0; x86_64) xterm-256color",
	"(Ubuntu 22.4.0; x86_64) screen-256color",
	"(Ubuntu 24.04.0; x86_64) xterm-256color",
	"(Ubuntu 24.04.0; arm64) xterm-256color",
	"(Mac OS X 14.7.0; arm64) iTerm.app",
	"(Mac OS X 15.1.0; arm64) iTerm.app",
	"(Windows 10.0.19045; x86_64) WindowsTerminal",
	"(Windows 11.0.26100; x86_64) WindowsTerminal",
}

var openAIEnvironmentFingerprintRandomInt = func(max *big.Int) (*big.Int, error) {
	return rand.Int(rand.Reader, max)
}

// NormalizeOpenAIEnvironmentFingerprint validates an administrator-supplied
// environment suffix. It intentionally permits custom values while rejecting
// control characters that could corrupt HTTP headers or logs.
func NormalizeOpenAIEnvironmentFingerprint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("environment fingerprint cannot be empty")
	}
	if len(value) > openAIEnvironmentFingerprintMaxLen {
		return "", fmt.Errorf("environment fingerprint must be at most %d bytes", openAIEnvironmentFingerprintMaxLen)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return "", errors.New("environment fingerprint must contain printable ASCII characters only")
		}
	}
	return value, nil
}

// SelectOpenAIEnvironmentFingerprint returns one built-in suffix. A failure
// in the OS random source degrades to the first stable candidate so account
// creation is not made unavailable by entropy plumbing.
func SelectOpenAIEnvironmentFingerprint() string {
	if len(openAIEnvironmentFingerprints) == 0 {
		return ""
	}
	index, err := openAIEnvironmentFingerprintRandomInt(big.NewInt(int64(len(openAIEnvironmentFingerprints))))
	if err != nil || index == nil || !index.IsInt64() || index.Sign() < 0 || index.Int64() >= int64(len(openAIEnvironmentFingerprints)) {
		slog.Warn("openai_environment_fingerprint_random_failed", "error", err)
		return openAIEnvironmentFingerprints[0]
	}
	return openAIEnvironmentFingerprints[index.Int64()]
}

// OpenAIEnvironmentFingerprintFromUserAgent extracts the suffix after the
// Codex version segment from a complete account-level User-Agent.
func OpenAIEnvironmentFingerprintFromUserAgent(userAgent string) string {
	ua := strings.TrimSpace(userAgent)
	if _, pairedUA, ok := openai.PairCodexClientIdentity(ua); !ok {
		return ""
	} else {
		ua = pairedUA
	}
	space := strings.IndexByte(ua, ' ')
	if space < 0 || space+1 >= len(ua) {
		return ""
	}
	fingerprint, err := NormalizeOpenAIEnvironmentFingerprint(ua[space+1:])
	if err != nil {
		return ""
	}
	return fingerprint
}

// BuildOpenAIUserAgentWithEnvironment builds a complete Codex UA from an
// optional existing candidate and an administrator-selected suffix. Valid
// client families are preserved; invalid candidates fall back to codex-tui.
func BuildOpenAIUserAgentWithEnvironment(candidateUA, fingerprint string) (string, error) {
	fingerprint, err := NormalizeOpenAIEnvironmentFingerprint(fingerprint)
	if err != nil {
		return "", err
	}

	canonical := codexCanonicalUserAgent()
	originator := openai.CodexDefaultOriginator
	if base := strings.TrimSpace(candidateUA); base != "" {
		if candidateOriginator, _, ok := openai.PairCodexClientIdentity(base); ok {
			originator = candidateOriginator
		}
	}
	version := codexClientVersionFromUA(canonical)
	return originator + "/" + version + " " + fingerprint, nil
}

// resolveOpenAIAccountUserAgent returns the persisted outbound UA for an
// account. Credential shadows share their parent's identity and never read a
// shadow-local user_agent value.
func resolveOpenAIAccountUserAgent(ctx context.Context, repo AccountRepository, account *Account) (string, error) {
	credentialAccount, err := resolveCredentialAccount(ctx, repo, account)
	if err != nil {
		return "", err
	}
	if credentialAccount == nil {
		return "", nil
	}
	return strings.TrimSpace(credentialAccount.GetOpenAIOutboundUserAgent()), nil
}

// resolveOpenAIAccountStoredUserAgent returns the credential owner's persisted
// candidate without consulting the process-wide canonical UA resolver. OAuth
// identity plans must combine this raw value with their request/connection
// snapshot so a settings refresh cannot change an already-open connection.
func resolveOpenAIAccountStoredUserAgent(ctx context.Context, repo AccountRepository, account *Account) (string, error) {
	credentialAccount, err := resolveCredentialAccount(ctx, repo, account)
	if err != nil {
		return "", err
	}
	if credentialAccount == nil {
		return "", nil
	}
	return strings.TrimSpace(credentialAccount.GetOpenAIUserAgent()), nil
}

func isOpenAIEnvironmentFingerprintAccount(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI &&
		(account.Type == AccountTypeOAuth || account.Type == AccountTypeAPIKey) &&
		!account.IsShadow()
}

// PrepareOpenAIAccountUserAgentForCreate assigns a fresh fingerprint to a new
// eligible account. It always replaces a client-provided suffix but keeps a
// recognized Codex client family.
func PrepareOpenAIAccountUserAgentForCreate(account *Account) {
	if !isOpenAIEnvironmentFingerprintAccount(account) {
		return
	}
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	} else {
		account.Credentials = maps.Clone(account.Credentials)
	}
	userAgent, err := BuildOpenAIUserAgentWithEnvironment(account.GetOpenAIUserAgent(), SelectOpenAIEnvironmentFingerprint())
	if err != nil {
		return
	}
	account.Credentials["user_agent"] = userAgent
}

// EnsureOpenAIAccountUserAgent fills legacy/conversion accounts without
// changing an existing account-level UA.
func EnsureOpenAIAccountUserAgent(account *Account) {
	if !isOpenAIEnvironmentFingerprintAccount(account) || strings.TrimSpace(account.GetOpenAIUserAgent()) != "" {
		return
	}
	PrepareOpenAIAccountUserAgentForCreate(account)
}
