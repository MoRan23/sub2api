package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestBuildAccountForCreateGeneratesUniqueOpenAIOAuthInstallationIDs(t *testing.T) {
	input := &CreateAccountInput{
		Name:     "OpenAI OAuth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	first, err := buildAccountForCreate(input, map[string]any{
		openAIPinnedInstallationIDKey:      "client-supplied",
		openAIInstallationRotateEnabledKey: true,
	})
	if err != nil {
		t.Fatalf("build first account: %v", err)
	}
	second, err := buildAccountForCreate(input, map[string]any{
		openAIPinnedInstallationIDKey:      "client-supplied",
		openAIInstallationRotateEnabledKey: true,
	})
	if err != nil {
		t.Fatalf("build second account: %v", err)
	}

	firstID, ok := first.Extra[openAIPinnedInstallationIDKey].(string)
	if !ok {
		t.Fatalf("first account missing generated installation_id: %#v", first.Extra)
	}
	secondID, ok := second.Extra[openAIPinnedInstallationIDKey].(string)
	if !ok {
		t.Fatalf("second account missing generated installation_id: %#v", second.Extra)
	}
	for name, value := range map[string]string{"first": firstID, "second": secondID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.Version() != 4 {
			t.Fatalf("%s installation_id must be UUID v4, got %q (err=%v)", name, value, err)
		}
	}
	if firstID == secondID {
		t.Fatalf("separate OpenAI OAuth accounts must not reuse installation_id %q", firstID)
	}
	if _, exists := first.Extra[openAIInstallationRotateEnabledKey]; exists {
		t.Fatal("legacy rotation option must be discarded")
	}
	if mode := first.Extra[codexFingerprintModeExtraKey]; mode != string(codexFingerprintOff) {
		t.Fatalf("new OpenAI OAuth account fingerprint mode = %#v, want off", mode)
	}
}

func TestBuildAccountForCreatePreservesExplicitCodexFingerprintMode(t *testing.T) {
	account, err := buildAccountForCreate(&CreateAccountInput{
		Name: "OpenAI OAuth", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
	}, map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)})
	if err != nil {
		t.Fatalf("build account: %v", err)
	}
	if mode := account.Extra[codexFingerprintModeExtraKey]; mode != string(codexFingerprintSession) {
		t.Fatalf("explicit fingerprint mode = %#v, want session", mode)
	}
}
