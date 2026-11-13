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
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func guardianTestContext(headers http.Header) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header = headers
	return c
}

func TestOpenAIGuardianExplicitMarkerAndFrozenCapture(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		kind    string
	}{
		{"absent", nil, ""},
		{"reviewer", http.Header{"X-Codex-Guardian": {"reviewer"}}, "reviewer"},
		{"classifier lowercase header", http.Header{"x-codex-guardian": {"classifier"}}, "classifier"},
		{"unknown", http.Header{"X-Codex-Guardian": {"future"}}, ""},
		{"whitespace value", http.Header{"X-Codex-Guardian": {" reviewer"}}, ""},
		{"uppercase value", http.Header{"X-Codex-Guardian": {"Reviewer"}}, ""},
		{"duplicate equal", http.Header{"X-Codex-Guardian": {"reviewer", "reviewer"}}, ""},
		{"duplicate conflicting", http.Header{"X-Codex-Guardian": {"reviewer", "classifier"}}, ""},
		{"duplicate casing", http.Header{"X-Codex-Guardian": {"reviewer"}, "x-codex-guardian": {"classifier"}}, ""},
		{"comma combined", http.Header{"X-Codex-Guardian": {"reviewer,classifier"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := guardianTestContext(tc.headers)
			capture := captureOpenAIGuardian(c)
			require.Equal(t, tc.kind, capture.kind)
			c.Request.Header = http.Header{"X-Codex-Guardian": {"reviewer"}}
			require.Equal(t, capture, captureOpenAIGuardian(c), "retries retain the original explicit marker")
			headers := http.Header{"x-codex-guardian": {"classifier"}, "X-Unrelated": {"keep"}}
			projectOpenAIGuardianHeader(c, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, headers)
			require.Equal(t, capture.values, headerValuesCaseInsensitive(headers, openAIGuardianHeader))
			require.Equal(t, "keep", headers.Get("X-Unrelated"))
		})
	}
}

func TestOpenAIGuardianFastPolicyPriorityAndAccountIsolation(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, &OpenAIFastPolicySettings{Rules: []OpenAIFastPolicyRule{{
		ServiceTier: OpenAIFastTierAny, Scope: BetaPolicyScopeAll, Action: BetaPolicyActionBlock,
		ErrorMessage: "blocked fast",
	}}})
	groupContext := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, ForceOpenAIFast: true,
	})
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, kind := range []string{"reviewer", "classifier"} {
		ctx := withOpenAIGuardianPolicyContext(groupContext, guardianTestContext(http.Header{"X-Codex-Guardian": {kind}}))
		for _, tier := range []string{"", `,"service_tier":"priority"`, `,"service_tier":null`, `,"service_tier":7`} {
			body := []byte(`{"type":"response.create","model":"gpt-6-astra","input":"keep"` + tier + `}`)
			original := bytes.Clone(body)
			out, err := svc.applyOpenAIFastPolicyToBody(ctx, oauth, "gpt-6-astra", body)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(out, "service_tier").Exists())
			ws, blocked, err := svc.applyOpenAIFastPolicyToWSResponseCreate(ctx, oauth, "gpt-6-astra", body)
			require.NoError(t, err)
			require.Nil(t, blocked)
			require.False(t, gjson.GetBytes(ws, "service_tier").Exists())
			require.Equal(t, original, body)
		}
		for _, account := range []*Account{
			{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_api_key_mode": "codex_engine"}},
			{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		} {
			require.Empty(t, openAIGuardianPolicyKind(ctx, account))
		}
		_, err := svc.applyOpenAIFastPolicyToBody(ctx, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
		require.Error(t, err, "API key keeps existing forced-Fast and rejection rules")
	}
	ctx := withOpenAIGuardianPolicyContext(groupContext, guardianTestContext(http.Header{"X-Codex-Guardian": {"reviewer", "classifier"}}))
	_, err := svc.applyOpenAIFastPolicyToBody(ctx, oauth, "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	require.Error(t, err, "an ambiguous marker cannot bypass the normal Fast policy")
}

func TestOpenAIGuardianFinalHTTPAndWSProjection(t *testing.T) {
	for _, kind := range []string{"reviewer", "classifier"} {
		for _, identityEnabled := range []bool{false, true} {
			for _, prewarm := range []bool{false, true} {
				t.Run(kind+"/identity="+strconvBool(identityEnabled)+"/prewarm="+strconvBool(prewarm), func(t *testing.T) {
					svc := &OpenAIGatewayService{}
					account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
					c := guardianTestContext(http.Header{"X-Codex-Guardian": {kind}})
					plan := codexWireProjectionTestPlan(t)
					plan.TurnIdentityRequested = identityEnabled
					plan.TurnIdentityEnabled = identityEnabled
					plan.WireProfile.ExtraMetadata = map[string]string{"guardian_credits_requested": "true", "parent_response_id": "should-not-nest"}
					payload := []byte(`{"type":"response.create","model":"gpt-final","instructions":"keep","service_tier":"priority","input":[],"client_metadata":{"guardian_credits_requested":"true","parent_response_id":"resp-unchanged","unknown":"keep"}}`)
					if prewarm {
						payload = bytes.Replace(payload, []byte(`"input":[]`), []byte(`"input":[],"generate":false`), 1)
					}
					original := bytes.Clone(payload)
					req := httptest.NewRequest(http.MethodPost, chatgptCodexURL, nil)
					req.Header.Set(openAIGuardianHeader, "override")
					httpBody, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, req, payload, OpenAIOAuthResponsesFinalizeOptions{Plan: plan, FinalModel: "gpt-final", FinalServiceTier: "priority"})
					require.NoError(t, err)
					require.Equal(t, kind, req.Header.Get(openAIGuardianHeader))
					require.Nil(t, extractOpenAIServiceTierFromBody(openAIUpstreamRequestBodySnapshot(req, payload)))
					wire, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.Equal(t, httpBody, wire)
					wsPlan, err := svc.finalizeOpenAIOAuthWSWirePlan(c, account, plan, payload, openAIOAuthWSWireFinalizeOptions{FinalModel: "gpt-final", FinalServiceTier: "priority"})
					require.NoError(t, err)
					wsBody, err := svc.projectOpenAIOAuthWSFrame(c, account, wsPlan, payload)
					require.NoError(t, err)
					for _, body := range [][]byte{httpBody, wsBody} {
						require.False(t, gjson.GetBytes(body, "service_tier").Exists())
						require.Equal(t, "true", gjson.GetBytes(body, "client_metadata.guardian_credits_requested").String())
						require.Equal(t, "resp-unchanged", gjson.GetBytes(body, "client_metadata.parent_response_id").String())
						require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
						nested := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
						require.False(t, gjson.Get(nested, "guardian_credits_requested").Exists())
						require.False(t, gjson.Get(nested, "parent_response_id").Exists())
					}
					wantHint := "model=gpt-final"
					if kind == "reviewer" {
						wantHint = ""
					}
					require.Equal(t, wantHint, req.Header.Get(openAICodexRoutingHintHeader))
					require.Equal(t, wantHint, wsPlan.WireProfile.RoutingHint)
					require.Equal(t, original, payload)
				})
			}
		}
	}
}

func strconvBool(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func TestOpenAIGuardianMetadataAndUnsupportedAccountRemainOpaque(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, raw := range []string{`null`, `true`, `7`, `{}`, `[]`, `"unexpected"`} {
		body := []byte(`{"model":"unknown-model","instructions":"keep","service_tier":"priority","input":[],"client_metadata":{"guardian_credits_requested":` + raw + `,"parent_response_id":` + raw + `}}`)
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
			account := &Account{Platform: PlatformOpenAI, Type: accountType}
			c := guardianTestContext(http.Header{"X-Codex-Guardian": {"reviewer"}})
			req := httptest.NewRequest(http.MethodPost, chatgptCodexURL, nil)
			req.Header.Set(openAIGuardianHeader, "account-override")
			out, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, req, body, OpenAIOAuthResponsesFinalizeOptions{Plan: codexWireProjectionTestPlan(t), FinalModel: "unknown-model", FinalServiceTier: "priority"})
			require.NoError(t, err)
			require.Equal(t, raw, gjson.GetBytes(out, "client_metadata.guardian_credits_requested").Raw)
			require.Equal(t, raw, gjson.GetBytes(out, "client_metadata.parent_response_id").Raw)
			if accountType == AccountTypeAPIKey {
				require.Equal(t, "priority", gjson.GetBytes(out, "service_tier").String())
				require.Equal(t, "account-override", req.Header.Get(openAIGuardianHeader))
			}
		}
	}
	for _, marker := range [][]string{nil, {"unknown"}, {"reviewer", "classifier"}} {
		c := guardianTestContext(http.Header{"X-Codex-Guardian": marker})
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		req := httptest.NewRequest(http.MethodPost, chatgptCodexURL, nil)
		req.Header.Set(openAIGuardianHeader, "reviewer")
		body := []byte(`{"model":"gpt-final","instructions":"keep","service_tier":"priority","input":[]}`)
		out, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-final", FinalServiceTier: "priority"})
		require.NoError(t, err)
		require.Equal(t, "priority", gjson.GetBytes(out, "service_tier").String())
		require.Equal(t, marker, headerValuesCaseInsensitive(req.Header, openAIGuardianHeader))
		require.False(t, gjson.GetBytes(out, "client_metadata.guardian_credits_requested").Exists())
		require.False(t, gjson.GetBytes(out, "client_metadata.parent_response_id").Exists())
	}
}

func TestOpenAIGuardianPoolCompatibilityStrictAndDeterministic(t *testing.T) {
	base := http.Header{"X-Codex-Guardian": {"reviewer"}}
	key := normalizeOpenAIWSHandshakeCompatibility(base, "identity")
	conn := &openAIWSConn{handshakeCompatibility: key}
	require.True(t, conn.matchesHandshakeCompatibility(key))
	for _, headers := range []http.Header{nil, {"X-Codex-Guardian": {"classifier"}}, {"X-Codex-Guardian": {"reviewer", "reviewer"}}} {
		other := normalizeOpenAIWSHandshakeCompatibility(headers, "identity")
		require.False(t, conn.matchesHandshakeCompatibility(other))
		require.False(t, conn.matchesContinuationCompatibility(other), "bound continuation must not tolerate Guardian changes")
	}
	advisory := key
	advisory.timingMetrics = "different"
	require.True(t, conn.matchesContinuationCompatibility(advisory))
	duplicates := http.Header{"X-Codex-Guardian": {"reviewer"}, "x-codex-guardian": {"classifier"}}
	for range 100 {
		require.Equal(t, `["classifier","reviewer"]`, openAIGuardianCompatibility(duplicates))
	}
	// A prewarm and an ordinary acquisition use exactly the same compatibility key.
	require.True(t, openAIWSAcquireRequestsMatchCompatibility(openAIWSAcquireRequest{Headers: base}, openAIWSAcquireRequest{Headers: base.Clone()}))
}

func TestOpenAIGuardianNeverRetainsStaleFastHint(t *testing.T) {
	for _, identity := range []bool{false, true} {
		plan := codexWireProjectionTestPlan(t)
		plan.TurnIdentityRequested = identity
		plan.WireProfile.GuardianRequestKind = "classifier"
		plan.WireProfile.RoutingHint = BuildOpenAICodexRoutingHint("gpt-old", "priority")
		result, err := FinalizeOpenAICodexWirePlanWithOptions(plan, FinalizeOpenAICodexWirePlanOptions{RequestKind: string(CodexWireRequestTurn), FinalServiceTier: "priority"})
		require.NoError(t, err)
		require.Empty(t, result.WireProfile.RoutingHint)
		require.Contains(t, plan.WireProfile.RoutingHint, "priority")
	}
}

func TestOpenAIGuardianObservationUsesPhysicalHeaderAndBoundedScalars(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	inbound := guardianTestContext(http.Header{"X-Codex-Guardian": {"reviewer"}})
	physical := http.Header{"X-Codex-Guardian": {"classifier"}, http.CanonicalHeaderKey(openAICodexRoutingHintHeader): {"model=gpt-final"}}
	body := []byte(`{"service_tier":"flex","client_metadata":{"guardian_credits_requested":"true","parent_response_id":"resp-secret-id"}}`)
	entry := buildFingerprintObservationEntry(inbound, account, installationIDResolution{}, physical, body, OpenAICodexTurnIdentity{}, false, false)
	require.Equal(t, "classifier", entry.GuardianRequest.Kind)
	require.True(t, entry.GuardianRequest.RoutingHintPresent)
	require.Equal(t, "flex", entry.GuardianRequest.ServiceTier)
	require.Equal(t, FingerprintGuardianField{Status: "valid", Type: "string", Present: true, Value: "true"}, entry.GuardianRequest.Credits)
	require.Equal(t, FingerprintGuardianField{Status: "valid", Type: "string", Present: true}, entry.GuardianRequest.ParentResponse)
	encoded, err := json.Marshal(entry.GuardianRequest)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "resp-secret-id")
	cloned := cloneFingerprintObservationEntry(entry)
	entry.GuardianRequest.Kind = "mutated"
	require.Equal(t, "classifier", cloned.GuardianRequest.Kind)
	for _, tc := range []struct{ raw, kind, status string }{
		{`null`, "null", "invalid"}, {`true`, "boolean", "invalid"}, {`0`, "number", "invalid"}, {`[]`, "array", "invalid"}, {`{}`, "object", "invalid"}, {`"false"`, "string", "valid"}, {`"secret-invalid-value"`, "string", "invalid"},
	} {
		field := observeOpenAIGuardianField(gjson.Parse(tc.raw), true)
		require.Equal(t, tc.kind, field.Type)
		require.Equal(t, tc.status, field.Status)
		if tc.status == "invalid" {
			require.Empty(t, field.Value)
		}
	}
	invalid := observeOpenAIGuardianRequest(account, http.Header{"X-Codex-Guardian": {"reviewer", "classifier"}}, []byte(`{"client_metadata":{"parent_response_id":"`+strings.Repeat("x", 2048)+`"}}`))
	require.Equal(t, "invalid", invalid.Status)
	require.False(t, invalid.Applied)
	require.Equal(t, "invalid", invalid.ParentResponse.Status)
	for _, marker := range [][]string{nil, {"reviewer"}, {"classifier"}, {"private-marker"}, {"reviewer", "classifier"}, {" reviewer"}} {
		headers := http.Header{"X-Codex-Guardian": marker, http.CanonicalHeaderKey(openAICodexRoutingHintHeader): {"private-affinity-value"}}
		snapshot := cloneFingerprintObservationHeaders(headers)
		require.Equal(t, observeOpenAIGuardianRequest(account, headers, nil), observeOpenAIGuardianRequest(account, snapshot, nil))
		encoded, err := json.Marshal(snapshot)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "private-marker")
		require.NotContains(t, string(encoded), "private-affinity-value")
	}
}

func TestOpenAIGuardianPhysicalTransportBuilders(t *testing.T) {
	for _, marker := range [][]string{nil, {"reviewer"}, {"classifier"}, {"future"}, {"reviewer", "classifier"}} {
		account := &Account{ID: 6007, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "fixture", "chatgpt_account_id": "fixture-account"}}
		svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account)}
		c := guardianTestContext(http.Header{"X-Codex-Guardian": marker})
		body := []byte(`{"model":"gpt-final","instructions":"keep","stream":true,"input":"hello","service_tier":"priority"}`)
		regular, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "fixture", true, "", false)
		require.NoError(t, err)
		passthrough, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "fixture")
		require.NoError(t, err)
		ws, resolution, err := svc.buildOpenAIWSHeadersWithBody(context.Background(), c, account, "fixture", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, "", "", "", body, true, "gpt-final", "priority")
		require.NoError(t, err)
		for _, headers := range []http.Header{regular.Header, passthrough.Header, ws} {
			require.Equal(t, marker, headerValuesCaseInsensitive(headers, openAIGuardianHeader))
			if openAIGuardianKind(c.Request.Header) == "reviewer" {
				require.Empty(t, headers.Get(openAICodexRoutingHintHeader))
			}
		}
		frame, err := svc.projectOpenAIOAuthWSFrame(c, account, resolution.OutboundIdentityPlan, body)
		require.NoError(t, err)
		for _, outbound := range [][]byte{openAIUpstreamRequestBodySnapshot(regular, body), openAIUpstreamRequestBodySnapshot(passthrough, body), frame} {
			require.Equal(t, openAIGuardianKind(c.Request.Header) == "", gjson.GetBytes(outbound, "service_tier").Exists())
		}
	}
}
