package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func newOpenAIOAuthPinAccount(id int64, extra map[string]any) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
}

func TestResolveOutboundInstallationIDDisabledFallsBackToPassthrough(t *testing.T) {
	acct := newOpenAIOAuthPinAccount(101, map[string]any{openAIInstallationPinEnabledKey: false})
	res := resolveOutboundInstallationID(acct, "client-abc")
	if res.Enabled || res.OutboundID != "" || res.ClientID != "client-abc" {
		t.Fatalf("unexpected disabled resolution: %+v", res)
	}
}

func TestResolveOutboundInstallationIDUsesPersistedUUIDAndIgnoresClient(t *testing.T) {
	const pinned = "11111111-2222-4333-8444-555555555555"
	acct := newOpenAIOAuthPinAccount(102, map[string]any{openAIPinnedInstallationIDKey: pinned})
	res := resolveOutboundInstallationID(acct, "99999999-8888-4777-8666-555555555555")
	if !res.Enabled || res.OutboundID != pinned {
		t.Fatalf("expected persisted UUID to win: %+v", res)
	}
}

func TestResolveOutboundInstallationIDRejectsNonV4PersistedValue(t *testing.T) {
	acct := newOpenAIOAuthPinAccount(103, map[string]any{openAIPinnedInstallationIDKey: "not-a-uuid"})
	res := resolveOutboundInstallationID(acct, "client-value")
	if !res.Enabled || res.OutboundID != "" || res.ClientID != "client-value" {
		t.Fatalf("invalid persisted value should require repair: %+v", res)
	}
}

func TestNormalizeCodexInstallationIDOnlyAcceptsV4(t *testing.T) {
	valid := "ABCDEF01-2345-4678-8ABC-DEF012345678"
	if got := normalizeCodexInstallationID(valid); got != "abcdef01-2345-4678-8abc-def012345678" {
		t.Fatalf("unexpected normalized UUID: %q", got)
	}
	if got := normalizeCodexInstallationID("11111111-2222-4111-8111-222222222222"); got == "" {
		t.Fatal("valid v4 UUID should be accepted")
	}
	if got := normalizeCodexInstallationID("11111111-2222-3111-8111-222222222222"); got != "" {
		t.Fatalf("v3 UUID should be rejected, got %q", got)
	}
}

func TestInstallationObservationRecordsOnlyWhenEnabled(t *testing.T) {
	svc := &OpenAIGatewayService{}
	SetInstallationObservationEnabled(false)
	acct := newOpenAIOAuthPinAccount(201, nil)
	svc.recordInstallationObservation(nil, acct, installationIDResolution{Enabled: true, OutboundID: "abc"}, http.Header{})
	if got := SnapshotInstallationObservations(10); len(got) != 0 {
		t.Fatalf("disabled observation must not record, got %d entries", len(got))
	}
	SetInstallationObservationEnabled(true)
	defer SetInstallationObservationEnabled(false)
	hdr := http.Header{}
	hdr.Set("User-Agent", "codex_cli_rs/2.0")
	hdr.Set("OpenAI-Beta", "responses=v1")
	hdr.Set("Originator", "codex_cli_rs")
	svc.recordInstallationObservation(nil, acct, installationIDResolution{Enabled: true, OutboundID: "outbound-1", ClientID: "client-1"}, hdr)
	entries := SnapshotInstallationObservations(10)
	if len(entries) != 1 || entries[0].OutboundInstallationID != "outbound-1" || entries[0].ClientReportedInstallationID != "client-1" {
		t.Fatalf("observation captured wrong ids: %+v", entries)
	}
	if entries[0].UserAgent != "codex_cli_rs/2.0" || entries[0].OpenAIBeta != "responses=v1" || entries[0].Originator != "codex_cli_rs" {
		t.Fatalf("observation captured wrong headers: %+v", entries[0])
	}
}

func TestInstallationObservationDisableClearsBuffer(t *testing.T) {
	svc := &OpenAIGatewayService{}
	SetInstallationObservationEnabled(true)
	acct := newOpenAIOAuthPinAccount(202, nil)
	svc.recordInstallationObservation(nil, acct, installationIDResolution{Enabled: true, OutboundID: uuid.NewString()}, http.Header{})
	if len(SnapshotInstallationObservations(10)) == 0 {
		t.Fatal("expected an entry before disabling")
	}
	SetInstallationObservationEnabled(false)
	if got := SnapshotInstallationObservations(10); len(got) != 0 {
		t.Fatalf("disabling observation should clear the buffer, got %d", len(got))
	}
}

func TestInstallationObservationUsesActualPassthroughHeader(t *testing.T) {
	svc := &OpenAIGatewayService{}
	SetInstallationObservationEnabled(true)
	defer SetInstallationObservationEnabled(false)
	acct := newOpenAIOAuthPinAccount(203, map[string]any{openAIInstallationPinEnabledKey: false})
	hdr := http.Header{}
	hdr.Set(codexInstallationIDKey, "actual-outbound")
	svc.recordInstallationObservation(nil, acct, installationIDResolution{
		ClientID: "client-reported",
	}, hdr)
	entries := SnapshotInstallationObservations(1)
	if len(entries) != 1 || entries[0].OutboundInstallationID != "actual-outbound" {
		t.Fatalf("observation should use actual passthrough header, got %+v", entries)
	}
}

func TestExtractClientInstallationIDHeaderThenBody(t *testing.T) {
	body := map[string]any{"client_metadata": map[string]any{codexInstallationIDKey: "from-body"}}
	if got := extractClientInstallationID(nil, body); got != "from-body" {
		t.Fatalf("expected body fallback, got %q", got)
	}
	if got := extractClientInstallationID(nil, nil); got != "" {
		t.Fatalf("expected empty when nothing present, got %q", got)
	}
}

func TestEnforceCodexInstallationIDInBody(t *testing.T) {
	body := map[string]any{"client_metadata": map[string]any{codexInstallationIDKey: "client-sent", "other": "keep-me"}}
	if !enforceCodexInstallationIDInBody(body, "owned-id") {
		t.Fatal("expected body mutated")
	}
	cm := body["client_metadata"].(map[string]any)
	if cm[codexInstallationIDKey] != "owned-id" || cm["other"] != "keep-me" {
		t.Fatalf("unexpected client metadata: %#v", cm)
	}
	if enforceCodexInstallationIDInBody(body, "owned-id") {
		t.Fatal("same value should be a no-op")
	}
}

func TestShouldRewriteOpenAIInstallationIDBoundaries(t *testing.T) {
	oauth := newOpenAIOAuthPinAccount(301, nil)
	if !shouldRewriteOpenAIInstallationID(oauth, false) {
		t.Fatal("OpenAI OAuth non-passthrough request should be eligible")
	}
	if shouldRewriteOpenAIInstallationID(oauth, true) {
		t.Fatal("passthrough request must not be eligible")
	}
	if shouldRewriteOpenAIInstallationID(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, false) {
		t.Fatal("API-key account must not be eligible")
	}
	if shouldRewriteOpenAIInstallationID(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, false) {
		t.Fatal("non-OpenAI account must not be eligible")
	}
}

func TestRewriteOpenAIInstallationIDInBodyAlsoRewritesNestedMetadata(t *testing.T) {
	const pinned = "11111111-2222-4333-8444-555555555555"
	body := map[string]any{
		"client_metadata": map[string]any{
			codexInstallationIDKey:     "client-top",
			openAIWSTurnMetadataHeader: `{"installation_id":"client-nested","session_id":"session-1","thread_id":"thread-1"}`,
		},
	}
	if !rewriteOpenAIInstallationIDInBody(body, pinned) {
		t.Fatal("expected body rewrite")
	}
	metadata := body["client_metadata"].(map[string]any)
	if metadata[codexInstallationIDKey] != pinned {
		t.Fatalf("top-level installation ID was not rewritten: %#v", metadata)
	}
	var nested map[string]any
	if err := json.Unmarshal([]byte(metadata[openAIWSTurnMetadataHeader].(string)), &nested); err != nil {
		t.Fatalf("nested metadata is not valid JSON: %v", err)
	}
	if nested[codexTurnMetadataInstallationIDKey] != pinned || nested["session_id"] != "session-1" || nested["thread_id"] != "thread-1" {
		t.Fatalf("unexpected nested metadata: %#v", nested)
	}
}

func TestRewriteOpenAIInstallationIDPreservesOpaqueOrMissingTurnMetadata(t *testing.T) {
	const pinned = "11111111-2222-4333-8444-555555555555"
	body := map[string]any{
		"client_metadata": map[string]any{openAIWSTurnMetadataHeader: "opaque-turn-metadata"},
	}
	if !rewriteOpenAIInstallationIDInBody(body, pinned) {
		t.Fatal("expected top-level installation ID to be added")
	}
	metadata := body["client_metadata"].(map[string]any)
	if metadata[openAIWSTurnMetadataHeader] != "opaque-turn-metadata" {
		t.Fatalf("opaque turn metadata was modified: %#v", metadata)
	}

	withoutTurn := map[string]any{}
	rewriteOpenAIInstallationIDInBody(withoutTurn, pinned)
	created := withoutTurn["client_metadata"].(map[string]any)
	if _, exists := created[openAIWSTurnMetadataHeader]; exists {
		t.Fatalf("turn metadata must not be synthesized: %#v", created)
	}
}

func TestRewriteOpenAIInstallationIDHeaders(t *testing.T) {
	const pinned = "11111111-2222-4333-8444-555555555555"
	headers := make(http.Header)
	headers.Set(codexInstallationIDKey, "client-top")
	headers.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested","session_id":"session-1"}`)
	headers.Set("x-codex-window-id", "window-1")
	if !rewriteOpenAIInstallationIDHeaders(headers, pinned) {
		t.Fatal("expected header rewrite")
	}
	if headers.Get(codexInstallationIDKey) != pinned || headers.Get("x-codex-window-id") != "window-1" {
		t.Fatalf("unexpected direct headers: %#v", headers)
	}
	var nested map[string]any
	if err := json.Unmarshal([]byte(headers.Get(openAIWSTurnMetadataHeader)), &nested); err != nil {
		t.Fatalf("nested header is not valid JSON: %v", err)
	}
	if nested[codexTurnMetadataInstallationIDKey] != pinned || nested["session_id"] != "session-1" {
		t.Fatalf("unexpected nested header: %#v", nested)
	}

	opaque := make(http.Header)
	opaque.Set(openAIWSTurnMetadataHeader, "opaque-turn-metadata")
	rewriteOpenAIInstallationIDHeaders(opaque, pinned)
	if opaque.Get(openAIWSTurnMetadataHeader) != "opaque-turn-metadata" {
		t.Fatalf("opaque header was modified: %#v", opaque)
	}
}

func TestCompactClientMetadataIsRemoved(t *testing.T) {
	body := map[string]any{"client_metadata": map[string]any{codexInstallationIDKey: "client"}, "model": "gpt-5"}
	if !stripOpenAICompactClientMetadata(body) {
		t.Fatal("expected compact metadata removal")
	}
	if _, exists := body["client_metadata"]; exists {
		t.Fatalf("compact body still contains client_metadata: %#v", body)
	}
	if stripOpenAICompactClientMetadata(body) {
		t.Fatal("second removal should be a no-op")
	}
}

func TestExtractClientInstallationIDFallsBackToNestedMetadata(t *testing.T) {
	body := map[string]any{
		"client_metadata": map[string]any{
			openAIWSTurnMetadataHeader: `{"installation_id":"nested-client","session_id":"session-1"}`,
		},
	}
	if got := extractClientInstallationID(nil, body); got != "nested-client" {
		t.Fatalf("expected nested client installation ID, got %q", got)
	}
}

func TestInstallationRequestCacheIsScopedToAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	first := newOpenAIOAuthPinAccount(401, map[string]any{
		openAIPinnedInstallationIDKey: "11111111-2222-4333-8444-555555555555",
	})
	second := newOpenAIOAuthPinAccount(402, map[string]any{
		openAIPinnedInstallationIDKey: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
	})
	svc := &OpenAIGatewayService{}

	firstResolution, err := svc.resolveInstallationIDForRequest(context.Background(), c, first, "first-client")
	if err != nil {
		t.Fatalf("resolve first account: %v", err)
	}
	secondResolution, err := svc.resolveInstallationIDForRequest(context.Background(), c, second, "second-client")
	if err != nil {
		t.Fatalf("resolve second account: %v", err)
	}
	if firstResolution.OutboundID == secondResolution.OutboundID {
		t.Fatalf("failover account reused cached installation ID: first=%q second=%q", firstResolution.OutboundID, secondResolution.OutboundID)
	}
	if secondResolution.OutboundID != second.GetPinnedOpenAIInstallationID() || secondResolution.ClientID != "second-client" {
		t.Fatalf("unexpected second account resolution: %+v", secondResolution)
	}
}

type installationIdentityRepoStub struct {
	AccountRepository
	accounts    map[int64]*Account
	mu          sync.Mutex
	persisted   map[int64]string
	ensureCalls int
}

func (r *installationIdentityRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if account := r.accounts[id]; account != nil {
		return account, nil
	}
	return nil, nil
}

func (r *installationIdentityRepoStub) EnsureOpenAIInstallationID(_ context.Context, accountID int64, _ string, generatedID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureCalls++
	if r.persisted == nil {
		r.persisted = make(map[int64]string)
	}
	if existing := r.persisted[accountID]; existing != "" {
		return existing, nil
	}
	r.persisted[accountID] = generatedID
	return generatedID, nil
}

func TestApplyOpenAIInstallationIDForOutboundRegularMaintainsBodyHeaderParity(t *testing.T) {
	const pinned = "11111111-2222-4333-8444-555555555555"
	account := newOpenAIOAuthPinAccount(501, map[string]any{openAIPinnedInstallationIDKey: pinned})
	body := map[string]any{
		"client_metadata": map[string]any{
			codexInstallationIDKey:     "body-client",
			openAIWSTurnMetadataHeader: `{"installation_id":"body-nested","turn":1}`,
		},
	}
	headers := make(http.Header)
	headers.Set(codexInstallationIDKey, "header-client")
	headers.Set(openAIWSTurnMetadataHeader, `{"installation_id":"header-nested","session":"s"}`)

	resolution, err := applyOpenAIInstallationIDForOutbound(context.Background(), nil, nil, account, body, headers, false, false)
	if err != nil {
		t.Fatalf("apply installation identity: %v", err)
	}
	if !resolution.Enabled || resolution.OutboundID != pinned {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
	metadata := body["client_metadata"].(map[string]any)
	if metadata[codexInstallationIDKey] != pinned {
		t.Fatalf("body direct installation ID not pinned: %#v", metadata)
	}
	var bodyNested map[string]any
	if err := json.Unmarshal([]byte(metadata[openAIWSTurnMetadataHeader].(string)), &bodyNested); err != nil {
		t.Fatalf("decode body nested metadata: %v", err)
	}
	if bodyNested[codexTurnMetadataInstallationIDKey] != pinned || bodyNested["turn"] != float64(1) {
		t.Fatalf("body nested metadata mismatch: %#v", bodyNested)
	}
	if headers.Get(codexInstallationIDKey) != pinned {
		t.Fatalf("header direct installation ID not pinned: %q", headers.Get(codexInstallationIDKey))
	}
	var headerNested map[string]any
	if err := json.Unmarshal([]byte(headers.Get(openAIWSTurnMetadataHeader)), &headerNested); err != nil {
		t.Fatalf("decode header nested metadata: %v", err)
	}
	if headerNested[codexTurnMetadataInstallationIDKey] != pinned || headerNested["session"] != "s" {
		t.Fatalf("header nested metadata mismatch: %#v", headerNested)
	}
}

func TestApplyOpenAIInstallationIDForOutboundCompactStripsBodyMetadata(t *testing.T) {
	const pinned = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	account := newOpenAIOAuthPinAccount(502, map[string]any{openAIPinnedInstallationIDKey: pinned})
	body := map[string]any{
		"model": "gpt-5.4",
		"client_metadata": map[string]any{codexInstallationIDKey: "body-client"},
	}
	headers := make(http.Header)
	headers.Set(openAIWSTurnMetadataHeader, `{"installation_id":"client-nested","session":"s"}`)

	if _, err := applyOpenAIInstallationIDForOutbound(context.Background(), nil, nil, account, body, headers, true, false); err != nil {
		t.Fatalf("apply compact installation identity: %v", err)
	}
	if _, exists := body["client_metadata"]; exists {
		t.Fatalf("compact body retained client_metadata: %#v", body)
	}
	if headers.Get(codexInstallationIDKey) != pinned {
		t.Fatalf("compact direct header mismatch: %q", headers.Get(codexInstallationIDKey))
	}
	if got := extractInstallationIDFromTurnMetadata(headers.Get(openAIWSTurnMetadataHeader)); got != pinned {
		t.Fatalf("compact nested header mismatch: %q", got)
	}
}

func TestApplyOpenAIInstallationIDForOutboundShadowUsesParentAndRepairsCAS(t *testing.T) {
	parentID := int64(503)
	parent := newOpenAIOAuthPinAccount(parentID, map[string]any{openAIPinnedInstallationIDKey: "invalid"})
	shadow := newOpenAIOAuthPinAccount(504, nil)
	shadow.ParentAccountID = &parentID
	repo := &installationIdentityRepoStub{accounts: map[int64]*Account{parentID: parent}}
	body := map[string]any{}
	headers := make(http.Header)

	resolution, err := applyOpenAIInstallationIDForOutbound(
		context.Background(), nil, repo, shadow, body, headers, false, false,
	)
	if err != nil {
		t.Fatalf("apply shadow installation identity: %v", err)
	}
	if !resolution.Enabled || resolution.OutboundID == "" || resolution.OutboundID == "invalid" || repo.ensureCalls != 1 {
		t.Fatalf("shadow CAS resolution mismatch: resolution=%+v ensure_calls=%d", resolution, repo.ensureCalls)
	}
	if got := body["client_metadata"].(map[string]any)[codexInstallationIDKey]; got != resolution.OutboundID {
		t.Fatalf("shadow body used wrong installation ID: %v", got)
	}
	if headers.Get(codexInstallationIDKey) != resolution.OutboundID {
		t.Fatalf("shadow header used wrong installation ID: %q", headers.Get(codexInstallationIDKey))
	}

	secondBody := map[string]any{}
	secondHeaders := make(http.Header)
	second, err := applyOpenAIInstallationIDForOutbound(context.Background(), nil, repo, shadow, secondBody, secondHeaders, false, false)
	if err != nil || second.OutboundID != resolution.OutboundID {
		t.Fatalf("shadow CAS value was not stable: first=%+v second=%+v err=%v", resolution, second, err)
	}
}

func TestApplyOpenAIInstallationIDForOutboundSkipsAPIKeyAndPassthrough(t *testing.T) {
	for name, account := range map[string]*Account{
		"api-key": {ID: 505, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		"passthrough": newOpenAIOAuthPinAccount(506, map[string]any{
			openAIPinnedInstallationIDKey: "11111111-2222-4333-8444-555555555555",
			"openai_passthrough":          true,
		}),
	} {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{"client_metadata": map[string]any{codexInstallationIDKey: "client"}}
			headers := make(http.Header)
			headers.Set(codexInstallationIDKey, "client")
			if _, err := applyOpenAIInstallationIDForOutbound(context.Background(), nil, nil, account, body, headers, false, account.IsOpenAIPassthroughEnabled()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if body["client_metadata"].(map[string]any)[codexInstallationIDKey] != "client" || headers.Get(codexInstallationIDKey) != "client" {
				t.Fatalf("installation metadata was rewritten for %s: body=%#v headers=%#v", name, body, headers)
			}
		})
	}
}
