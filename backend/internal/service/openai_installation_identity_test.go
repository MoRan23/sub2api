package service

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func newOpenAIOAuthPinAccount(id int64, extra map[string]any) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    extra,
	}
}

func resetPinRegistry() {
	globalInstallationPinRegistry.mu.Lock()
	globalInstallationPinRegistry.values = make(map[int64]string)
	globalInstallationPinRegistry.mu.Unlock()
}

func TestResolveOutboundInstallationID_DisabledFallsBackToPassthrough(t *testing.T) {
	resetPinRegistry()
	acct := newOpenAIOAuthPinAccount(101, map[string]any{
		openAIInstallationPinEnabledKey: false,
	})
	res := resolveOutboundInstallationID(acct, "client-abc")
	if res.Enabled {
		t.Fatalf("expected pinning disabled, got Enabled=true")
	}
	if res.OutboundID != "" {
		t.Fatalf("disabled pinning must not emit an outbound id, got %q", res.OutboundID)
	}
	if res.ClientID != "client-abc" {
		t.Fatalf("client id should be preserved for observation, got %q", res.ClientID)
	}
}

func TestResolveOutboundInstallationID_NonOpenAIAccountDisabled(t *testing.T) {
	resetPinRegistry()
	acct := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	res := resolveOutboundInstallationID(acct, "client-abc")
	if res.Enabled {
		t.Fatalf("non-OpenAI account must never pin")
	}
}

func TestResolveOutboundInstallationID_SeizesFirstClientValueAndStaysStable(t *testing.T) {
	resetPinRegistry()
	acct := newOpenAIOAuthPinAccount(102, nil) // nil extra => default ON

	// Codex only ever persists a valid UUID, so the value a real client reports
	// is a canonical v4 UUID. Seizing it must return it verbatim.
	const clientUUID = "11111111-2222-4333-8444-555555555555"
	first := resolveOutboundInstallationID(acct, clientUUID)
	if !first.Enabled {
		t.Fatalf("default (absent key) should enable pinning")
	}
	if first.OutboundID != clientUUID {
		t.Fatalf("first request should seize client value, got %q", first.OutboundID)
	}
	if !first.NeedsPersist {
		t.Fatalf("first seizure should request persistence")
	}

	// A later request reporting a DIFFERENT client value must still emit the
	// seized value from the registry.
	second := resolveOutboundInstallationID(acct, "99999999-8888-4777-8666-555555555555")
	if second.OutboundID != clientUUID {
		t.Fatalf("subsequent request must reuse seized value, got %q", second.OutboundID)
	}
	// The DB copy on this in-memory account was never updated (async persist does
	// not mutate the struct), so reconciliation legitimately still wants a write.
	if !second.NeedsPersist {
		t.Fatalf("registry hit with a stale/empty DB copy should reconcile (NeedsPersist)")
	}

	// Once the account's persisted value matches the registry, no re-persist.
	acct.Extra = map[string]any{openAIPinnedInstallationIDKey: clientUUID}
	third := resolveOutboundInstallationID(acct, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	if third.OutboundID != clientUUID {
		t.Fatalf("third request must reuse seized value, got %q", third.OutboundID)
	}
	if third.NeedsPersist {
		t.Fatalf("registry hit with matching DB copy must not re-persist")
	}
}

func TestResolveOutboundInstallationID_NormalizesSeizedUUIDToLowercase(t *testing.T) {
	// Codex's resolve_installation_id returns Uuid::parse_str(..).to_string(),
	// which lowercases. A client that reports an uppercase UUID must be pinned to
	// the canonical lowercase form so the outbound value matches a real client.
	resetPinRegistry()
	acct := newOpenAIOAuthPinAccount(107, nil)
	res := resolveOutboundInstallationID(acct, "ABCDEF01-2345-4678-8ABC-DEF012345678")
	want := "abcdef01-2345-4678-8abc-def012345678"
	if res.OutboundID != want {
		t.Fatalf("seized UUID should be canonicalized to lowercase, got %q", res.OutboundID)
	}
}

func TestResolveOutboundInstallationID_RegeneratesWhenClientValueNotUUID(t *testing.T) {
	// Codex discards non-UUID installation_id file contents and mints a fresh v4
	// (see resolve_installation_id_rewrites_invalid_file_contents). A junk client
	// value must therefore NOT be forwarded; a valid v4 UUID must be generated.
	resetPinRegistry()
	acct := newOpenAIOAuthPinAccount(108, nil)
	res := resolveOutboundInstallationID(acct, "not-a-uuid")
	if res.OutboundID == "not-a-uuid" {
		t.Fatalf("non-UUID client value must not be seized verbatim")
	}
	parsed, err := uuid.Parse(res.OutboundID)
	if err != nil {
		t.Fatalf("regenerated id must be a valid uuid: %v", err)
	}
	if parsed.Version() != 4 {
		t.Fatalf("regenerated id must be uuid v4, got v%d", parsed.Version())
	}
}

func TestResolveOutboundInstallationID_GeneratesUUIDv4WhenClientSendsNothing(t *testing.T) {
	resetPinRegistry()
	acct := newOpenAIOAuthPinAccount(103, nil)

	res := resolveOutboundInstallationID(acct, "")
	if res.OutboundID == "" {
		t.Fatalf("expected a generated installation id")
	}
	parsed, err := uuid.Parse(res.OutboundID)
	if err != nil {
		t.Fatalf("generated id must be a valid uuid: %v", err)
	}
	if parsed.Version() != 4 {
		t.Fatalf("generated id must be uuid v4 (matching real Codex), got v%d", parsed.Version())
	}
	if !res.NeedsPersist {
		t.Fatalf("generated seizure should request persistence")
	}
}

func TestResolveOutboundInstallationID_PersistedSeedsRegistry(t *testing.T) {
	resetPinRegistry()
	acct := newOpenAIOAuthPinAccount(104, map[string]any{
		openAIPinnedInstallationIDKey: "persisted-xyz",
	})
	res := resolveOutboundInstallationID(acct, "ignored-client")
	if res.OutboundID != "persisted-xyz" {
		t.Fatalf("persisted value should win over client value, got %q", res.OutboundID)
	}
	if v, ok := globalInstallationPinRegistry.get(104); !ok || v != "persisted-xyz" {
		t.Fatalf("persisted value should seed the registry, got %q ok=%v", v, ok)
	}
}

func TestResolveOutboundInstallationID_RotationMintsFreshWithoutTouchingRegistry(t *testing.T) {
	resetPinRegistry()
	// Seed a stable pin first.
	globalInstallationPinRegistry.set(105, "stable-pin")
	acct := newOpenAIOAuthPinAccount(105, map[string]any{
		openAIInstallationRotateEnabledKey: true,
	})

	a := resolveOutboundInstallationID(acct, "client-1")
	b := resolveOutboundInstallationID(acct, "client-2")
	if !a.Rotated || !b.Rotated {
		t.Fatalf("rotation should mark results Rotated")
	}
	if a.OutboundID == b.OutboundID {
		t.Fatalf("rotation should mint distinct values, got %q twice", a.OutboundID)
	}
	if _, err := uuid.Parse(a.OutboundID); err != nil {
		t.Fatalf("rotated id must be a valid uuid: %v", err)
	}
	if a.NeedsPersist || b.NeedsPersist {
		t.Fatalf("rotation must not request persistence")
	}
	// The stable pin in the registry must be untouched so turning rotation
	// back off resumes it.
	if v, ok := globalInstallationPinRegistry.get(105); !ok || v != "stable-pin" {
		t.Fatalf("rotation must not disturb the registry, got %q ok=%v", v, ok)
	}
}

func TestClearPinnedInstallationIDFromRegistry(t *testing.T) {
	resetPinRegistry()
	globalInstallationPinRegistry.set(106, "to-clear")
	ClearPinnedInstallationIDFromRegistry(106)
	if _, ok := globalInstallationPinRegistry.get(106); ok {
		t.Fatalf("expected registry entry cleared")
	}
}

func TestExtractClientInstallationID_HeaderThenBody(t *testing.T) {
	// Body fallback when no header.
	body := map[string]any{
		"client_metadata": map[string]any{
			codexInstallationIDKey: "from-body",
		},
	}
	if got := extractClientInstallationID(nil, body); got != "from-body" {
		t.Fatalf("expected body fallback, got %q", got)
	}
	if got := extractClientInstallationID(nil, nil); got != "" {
		t.Fatalf("expected empty when nothing present, got %q", got)
	}
}

func TestEnforceCodexInstallationIDInBody_OverwritesClientValue(t *testing.T) {
	body := map[string]any{
		"client_metadata": map[string]any{
			codexInstallationIDKey: "client-sent",
			"other":                "keep-me",
		},
	}
	mutated := enforceCodexInstallationIDInBody(body, "owned-id")
	if !mutated {
		t.Fatalf("expected body mutated")
	}
	cm := body["client_metadata"].(map[string]any)
	if cm[codexInstallationIDKey] != "owned-id" {
		t.Fatalf("installation id should be overwritten, got %q", cm[codexInstallationIDKey])
	}
	if cm["other"] != "keep-me" {
		t.Fatalf("sibling client_metadata keys must be preserved")
	}
	// Idempotent: calling again with the same value reports no change.
	if enforceCodexInstallationIDInBody(body, "owned-id") {
		t.Fatalf("second identical enforce should be a no-op")
	}
}

func TestEnforceCodexInstallationIDInBody_CreatesMetadataWhenAbsent(t *testing.T) {
	body := map[string]any{}
	if !enforceCodexInstallationIDInBody(body, "owned-id") {
		t.Fatalf("expected body mutated")
	}
	cm, ok := body["client_metadata"].(map[string]any)
	if !ok || cm[codexInstallationIDKey] != "owned-id" {
		t.Fatalf("expected client_metadata created with owned id, got %#v", body["client_metadata"])
	}
}

func TestInstallationObservation_RecordsOnlyWhenEnabled(t *testing.T) {
	svc := &OpenAIGatewayService{}
	// Disabled: nothing recorded.
	SetInstallationObservationEnabled(false)
	acct := newOpenAIOAuthPinAccount(201, nil)
	hdr := http.Header{}
	hdr.Set("User-Agent", "codex_cli_rs/1.0")
	svc.recordInstallationObservation(nil, acct, installationIDResolution{Enabled: true, OutboundID: "abc"}, hdr)
	if got := SnapshotInstallationObservations(10); len(got) != 0 {
		t.Fatalf("disabled observation must not record, got %d entries", len(got))
	}

	// Enabled: records with captured fields.
	SetInstallationObservationEnabled(true)
	defer SetInstallationObservationEnabled(false)
	hdr2 := http.Header{}
	hdr2.Set("User-Agent", "codex_cli_rs/2.0")
	hdr2.Set("OpenAI-Beta", "responses=v1")
	hdr2.Set("Originator", "codex_cli_rs")
	svc.recordInstallationObservation(nil, acct, installationIDResolution{
		Enabled:    true,
		OutboundID: "outbound-1",
		ClientID:   "client-1",
	}, hdr2)

	entries := SnapshotInstallationObservations(10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 recorded entry, got %d", len(entries))
	}
	e := entries[0]
	if e.OutboundInstallationID != "outbound-1" || e.ClientReportedInstallationID != "client-1" {
		t.Fatalf("observation captured wrong ids: %+v", e)
	}
	if e.UserAgent != "codex_cli_rs/2.0" || e.OpenAIBeta != "responses=v1" {
		t.Fatalf("observation captured wrong headers: %+v", e)
	}
	if e.Originator != "codex_cli_rs" {
		t.Fatalf("observation captured wrong originator: %+v", e)
	}
	if e.AccountID != 201 {
		t.Fatalf("observation captured wrong account id: %+v", e)
	}
}

func TestInstallationObservation_DisableClearsBuffer(t *testing.T) {
	svc := &OpenAIGatewayService{}
	SetInstallationObservationEnabled(true)
	acct := newOpenAIOAuthPinAccount(202, nil)
	svc.recordInstallationObservation(nil, acct, installationIDResolution{Enabled: true, OutboundID: "x"}, http.Header{})
	if len(SnapshotInstallationObservations(10)) == 0 {
		t.Fatalf("expected an entry before disabling")
	}
	SetInstallationObservationEnabled(false)
	if got := SnapshotInstallationObservations(10); len(got) != 0 {
		t.Fatalf("disabling observation should clear the buffer, got %d", len(got))
	}
}
