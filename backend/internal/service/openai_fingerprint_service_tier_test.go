package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func serviceTierFingerprintFixture(t *testing.T) (*gin.Context, context.Context, *Account) {
	t.Helper()
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := (&OpenAIGatewayService{}).freezeOpenAIRequestPolicy(context.Background(), c)
	return c, ctx, &Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
}

func TestFingerprintServiceTierPolicyUsesFinalPhysicalBody(t *testing.T) {
	c, ctx, account := serviceTierFingerprintFixture(t)
	for _, tc := range []struct {
		name       string
		body       string
		capability string
		wantTier   string
	}{
		{"forced", `{"model":"gpt-6-astra","service_tier":"ultrafast"}`, "supported", "ultrafast"},
		{"global_filter", `{"model":"gpt-6-astra"}`, "supported", ""},
		{"global_force_priority", `{"service_tier":"priority"}`, "supported", "priority"},
		{"unsupported_retains_client", `{"service_tier":"flex"}`, "unsupported", "flex"},
		{"discovery_failure_retains_client", `{"service_tier":"flex"}`, "unavailable", "flex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordOpenAIServiceTierPolicy(ctx, account, "account", "flex", "ultrafast", tc.capability)
			entry := buildFingerprintObservationEntry(c, account, installationIDResolution{}, nil, []byte(tc.body), OpenAICodexTurnIdentity{}, false, false)
			require.Equal(t, &FingerprintServiceTierPolicy{Source: "account", RequestedTier: "flex", ForcedTier: "ultrafast", EffectiveTier: tc.wantTier, Capability: tc.capability}, entry.ServiceTierPolicy)
		})
	}
}

func TestFingerprintServiceTierPolicyGuardianAndHandshake(t *testing.T) {
	c, ctx, account := serviceTierFingerprintFixture(t)
	account.Type = AccountTypeOAuth
	recordOpenAIServiceTierPolicy(ctx, account, "none", "ultrafast", "", "guardian")
	headers := http.Header{"X-Codex-Guardian": {"reviewer"}}
	entry := buildFingerprintObservationEntry(c, account, installationIDResolution{}, headers, []byte(`{"model":"gpt-6-astra"}`), OpenAICodexTurnIdentity{}, false, false)
	require.Equal(t, "reviewer", entry.GuardianRequest.Kind)
	require.Equal(t, &FingerprintServiceTierPolicy{Source: "none", RequestedTier: "ultrafast", Capability: "guardian"}, entry.ServiceTierPolicy)
	handshake := buildFingerprintObservationEntry(c, account, installationIDResolution{}, headers, nil, OpenAICodexTurnIdentity{}, false, false)
	require.Nil(t, handshake.ServiceTierPolicy)
}

func TestFingerprintServiceTierPolicySnapshotOwnershipAndAttribution(t *testing.T) {
	c, ctx, account := serviceTierFingerprintFixture(t)
	recordOpenAIServiceTierPolicy(ctx, account, "group", "fast", "ultrafast", "supported")
	entry := buildFingerprintObservationEntry(c, account, installationIDResolution{}, nil, []byte(`{"service_tier":"ultrafast"}`), OpenAICodexTurnIdentity{}, false, false)
	require.Equal(t, "priority", entry.ServiceTierPolicy.RequestedTier)
	cloned := cloneFingerprintObservationEntry(entry)
	entry.ServiceTierPolicy.Capability = "mutated"
	require.Equal(t, "supported", cloned.ServiceTierPolicy.Capability)

	other := &Account{ID: account.ID + 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.Nil(t, fingerprintServiceTierPolicyFromContext(c, other, true))
	recordOpenAIServiceTierPolicy(ctx, other, "account", "", "priority", "not_required")
	require.Nil(t, fingerprintServiceTierPolicyFromContext(c, account, true))
	require.Equal(t, "ultrafast", cloned.ServiceTierPolicy.ForcedTier)
	require.Equal(t, "priority", fingerprintServiceTierPolicyFromContext(c, other, true).ForcedTier)

	// Repeated policy freezing reuses the request capture without losing its value.
	refrozen := (&OpenAIGatewayService{}).freezeOpenAIRequestPolicy(ctx, c)
	require.Same(t, ctx.Value(openAIServiceTierPolicyContextKey{}), refrozen.Value(openAIServiceTierPolicyContextKey{}))
}

func TestFingerprintServiceTierPolicyNeverRetainsUnknownValues(t *testing.T) {
	c, ctx, account := serviceTierFingerprintFixture(t)
	secret := "secret-" + strings.Repeat("x", 1024)
	recordOpenAIServiceTierPolicy(ctx, account, secret, secret, secret, secret)
	entry := buildFingerprintObservationEntry(c, account, installationIDResolution{}, nil, []byte(`{"service_tier":"secret-value"}`), OpenAICodexTurnIdentity{}, false, false)
	require.Equal(t, &FingerprintServiceTierPolicy{Source: "none", Capability: "unavailable"}, entry.ServiceTierPolicy)
	raw, err := json.Marshal(entry.ServiceTierPolicy)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
}

func TestFingerprintServiceTierPolicyFramesDoNotShareGinDecision(t *testing.T) {
	c, ctx, account := serviceTierFingerprintFixture(t)
	recordOpenAIServiceTierPolicy(ctx, account, "group", "", "priority", "not_required")
	firstCtx := withOpenAIServiceTierPolicyFrameContext(ctx)
	recordOpenAIServiceTierPolicy(firstCtx, account, "account", "flex", "ultrafast", "supported")
	first := snapshotOpenAIServiceTierPolicy(firstCtx, account)
	secondCtx := withOpenAIServiceTierPolicyFrameContext(ctx)
	require.Nil(t, snapshotOpenAIServiceTierPolicy(secondCtx, account))
	recordOpenAIServiceTierPolicy(secondCtx, account, "none", "priority", "", "guardian")
	second := snapshotOpenAIServiceTierPolicy(secondCtx, account)

	// A delayed first physical write uses its own frozen decision even after the
	// next frame or the HTTP session has published a different one.
	firstEntry := buildFingerprintObservationEntry(c, account, installationIDResolution{}, nil,
		[]byte(`{"service_tier":"ultrafast"}`), OpenAICodexTurnIdentity{}, false, false, first)
	secondEntry := buildFingerprintObservationEntry(c, account, installationIDResolution{}, nil,
		[]byte(`{}`), OpenAICodexTurnIdentity{}, false, false, second)
	require.Equal(t, "account", firstEntry.ServiceTierPolicy.Source)
	require.Equal(t, "ultrafast", firstEntry.ServiceTierPolicy.EffectiveTier)
	require.Equal(t, "guardian", secondEntry.ServiceTierPolicy.Capability)
	require.Empty(t, secondEntry.ServiceTierPolicy.EffectiveTier)
	first.Capability = "mutated"
	require.Equal(t, "supported", firstEntry.ServiceTierPolicy.Capability)
	noPolicyEntry := buildFingerprintObservationEntry(c, account, installationIDResolution{}, nil,
		[]byte(`{}`), OpenAICodexTurnIdentity{}, false, false, nil)
	require.Nil(t, noPolicyEntry.ServiceTierPolicy)

	bridgeCtx := withOpenAIServiceTierPolicySnapshot(ctx, account, second)
	bridgeCtx = (&OpenAIGatewayService{}).freezeOpenAIRequestPolicy(bridgeCtx, c)
	require.Equal(t, "guardian", snapshotOpenAIServiceTierPolicy(bridgeCtx, account).Capability)
	require.Equal(t, "group", fingerprintServiceTierPolicyFromContext(c, account, true).Source)
}

func TestFingerprintServiceTierPolicyFramePublicationFreezesEvidence(t *testing.T) {
	c, ctx, account := serviceTierFingerprintFixture(t)
	firstCtx := withOpenAIServiceTierPolicyFrameContext(ctx)
	recordOpenAIServiceTierPolicy(firstCtx, account, "account", "flex", "ultrafast", "supported")
	first := snapshotOpenAIServiceTierPolicy(firstCtx, account)
	s := &OpenAIGatewayService{}
	publishFirst := s.freezeFingerprintObservationWSFrameWithTierPolicy(c, account, nil,
		[]byte(`{"type":"response.create","service_tier":"ultrafast"}`), nil, nil, first)
	// Later policy decisions and mutation of a caller's snapshot must not affect
	// the earlier frame after the physical-send view has already been frozen.
	first.Capability = "mutated"
	secondCtx := withOpenAIServiceTierPolicyFrameContext(ctx)
	recordOpenAIServiceTierPolicy(secondCtx, account, "none", "priority", "", "guardian")
	publishSecond := s.freezeFingerprintObservationWSFrameWithTierPolicy(c, account, nil,
		[]byte(`{"type":"response.create"}`), nil, nil, snapshotOpenAIServiceTierPolicy(secondCtx, account))
	publishSecond()
	publishFirst()
	entries := SnapshotFingerprintObservations(10)
	require.Len(t, entries, 2)
	var supported, guardian *FingerprintServiceTierPolicy
	for _, entry := range entries {
		require.Equal(t, FingerprintObservationEventWSFrame, entry.EventKind)
		require.NotNil(t, entry.ServiceTierPolicy)
		switch entry.ServiceTierPolicy.Capability {
		case "supported":
			supported = entry.ServiceTierPolicy
		case "guardian":
			guardian = entry.ServiceTierPolicy
		}
	}
	require.NotNil(t, supported)
	require.Equal(t, "ultrafast", supported.EffectiveTier)
	require.NotNil(t, guardian)
	require.Empty(t, guardian.EffectiveTier)
}

func TestFingerprintServiceTierPolicyDisabledDoesNotAllocateCapture(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := withOpenAIServiceTierPolicyContext(context.Background(), c)
	_, present := c.Get(openAIServiceTierPolicyCaptureKey)
	require.False(t, present)
	require.Nil(t, ctx.Value(openAIServiceTierPolicyContextKey{}))
	recordOpenAIServiceTierPolicy(ctx, &Account{ID: 1}, "account", "flex", "priority", "not_required")
	require.Nil(t, fingerprintServiceTierPolicyFromContext(c, &Account{ID: 1}, true))
}

func TestFingerprintServiceTierPolicyEngineRecordsOnePhysicalAttempt(t *testing.T) {
	c, _, _ := serviceTierFingerprintFixture(t)
	body := []byte(`{"model":"alias","service_tier":"flex","input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	account := engineAccount()
	account.ID = 19
	account.Extra[ForceOpenAIFastExtraKey] = true
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(`{"id":"resp_engine","usage":{"input_tokens":1,"output_tokens":1}}`))}}
	s := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	_, err := s.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	entries := SnapshotFingerprintObservations(10)
	require.Len(t, entries, 1)
	require.Equal(t, account.ID, entries[0].AccountID)
	require.Equal(t, FingerprintObservationEventHTTP, entries[0].EventKind)
	require.Equal(t, &FingerprintServiceTierPolicy{Source: "account", RequestedTier: "flex", ForcedTier: "priority", EffectiveTier: "priority", Capability: "not_required"}, entries[0].ServiceTierPolicy)
}
