package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCaptureOpenAIOAuthIdentityKeepsFixedPriorityAndExplicitAliases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	c.Request.Header.Set("session-id", "header-session")
	c.Request.Header.Set("thread-id", "header-thread")
	body := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"metadata-session\",\"thread_id\":\"metadata-thread\"}"},"prompt_cache_key":"fallback"}`)

	capture := CaptureOpenAIOAuthIdentity(c, body, "caller-fallback")
	require.Equal(t, "metadata-session", capture.Logical.SessionKey)
	require.Equal(t, "metadata-thread", capture.Logical.ThreadKey)
	require.True(t, capture.Logical.Explicit)
	require.Len(t, capture.Aliases, 1)
	require.Equal(t, "metadata-session", capture.Aliases[0].SessionKey)
	for _, alias := range capture.Aliases {
		require.True(t, alias.Explicit)
		require.NotEqual(t, "fallback", alias.SessionKey)
		require.NotEqual(t, "caller-fallback", alias.SessionKey)
	}
}

func TestCaptureOpenAIOAuthIdentityWithEndpointAliasAppendsNonExplicitLowestPriorityAlias(t *testing.T) {
	body := []byte(`{"client_metadata":{"session_id":"canonical-session","thread_id":"canonical-thread"}}`)
	capture := CaptureOpenAIOAuthIdentityWithEndpointAlias(nil, body, "alpha-id")

	require.Equal(t, "canonical-session", capture.Logical.SessionKey)
	require.Len(t, capture.Aliases, 2)
	require.Equal(t, "canonical-session", capture.Aliases[0].SessionKey)
	require.True(t, capture.Aliases[0].Explicit)
	require.Equal(t, "alpha-id", capture.Aliases[1].SessionKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourceCallerSeed, capture.Aliases[1].Source)
	require.False(t, capture.Aliases[1].Explicit)
	require.Equal(t, 1, capture.Aliases[1].Priority)
}

func TestCaptureOpenAIOAuthIdentityFallbackAliasIsNotExplicit(t *testing.T) {
	capture := CaptureOpenAIOAuthIdentity(nil, []byte(`{"prompt_cache_key":"prompt"}`), "caller")
	require.Equal(t, "caller", capture.Logical.SessionKey)
	require.False(t, capture.Logical.Explicit)
	require.Len(t, capture.Aliases, 1)
	require.False(t, capture.Aliases[0].Explicit)
}

func TestCaptureOpenAIOAuthIdentityLabelsBodyPromptCacheFallback(t *testing.T) {
	capture := CaptureOpenAIOAuthIdentity(nil, []byte(`{"prompt_cache_key":"prompt"}`), "")
	require.Equal(t, "prompt", capture.Logical.SessionKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourcePromptCacheKey, capture.Logical.Source)
	require.False(t, capture.Logical.Explicit)
	require.Len(t, capture.Aliases, 1)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourcePromptCacheKey, capture.Aliases[0].Source)
}

func TestGenerateSessionHashForOpenAIOAuthIdentityUsesCapturedLogicalSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(`{"model":"gpt-5.4","client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"captured-session\",\"thread_id\":\"captured-thread\"}"}}`)
	SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))

	got := (&OpenAIGatewayService{}).GenerateSessionHashForOpenAIOAuthIdentity(c, body, "fallback")
	require.Equal(t, DeriveSessionHashFromSeed("captured-session"), got)
	require.NotEqual(t, DeriveSessionHashFromSeed("captured-thread"), got)
}

func TestGenerateSessionHashForOpenAIOAuthIdentityKeepsLegacyFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))

	svc := &OpenAIGatewayService{}
	require.Equal(t, svc.GenerateSessionHash(c, body), svc.GenerateSessionHashForOpenAIOAuthIdentity(c, body, "fallback"))
}

func TestCaptureOpenAIOAuthIdentityCountsAndIgnoresInvalidTurnMetadataCarriers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `opaque-header`)
	c.Request.Header.Set("session-id", "canonical-session")
	body := []byte(`{
		"x-codex-turn-metadata": null,
		"client_metadata": {"x-codex-turn-metadata": "opaque-client"}
	}`)
	before := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()

	capture := CaptureOpenAIOAuthIdentityWithTurnMetadata(c, body, "fallback", `["opaque-ws"]`)

	require.Equal(t, 4, capture.InvalidMetadataCount)
	require.Equal(t, "canonical-session", capture.Logical.SessionKey)
	require.Equal(t, OpenAIOutboundSessionLogicalKeySourceHeaderSession, capture.Logical.Source)
	require.Len(t, capture.Aliases, 1)
	require.Equal(t, "canonical-session", capture.Aliases[0].SessionKey)
	after := SnapshotOpenAIOutboundSessionIdentityRuntimeMetrics()
	require.Equal(t, int64(4), after.InvalidMetadataTotal-before.InvalidMetadataTotal)
}

func TestApplyOpenAIOAuthIdentityPlanExistingTurnMetadataOnly(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	headers := http.Header{"X-Codex-Turn-Metadata": {`{"session_id":"old","thread_id":"old"}`}}
	body := []byte(`{"model":"gpt","client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"old\",\"thread_id\":\"old\"}"}}`)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		ProjectionMode:     OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	require.Empty(t, headers.Get("session-id"))
	require.Contains(t, headers.Get("X-Codex-Turn-Metadata"), id.SessionID)
	require.Contains(t, string(out), id.SessionID)
	require.NotContains(t, string(out), `"session_id":"`+id.SessionID+`"`)
}

func TestApplyOpenAIOAuthIdentityPlanExistingTurnMetadataOnlyDoesNotCreateBodyMetadata(t *testing.T) {
	body := []byte(" { \"model\" : \"gpt\" } \n")
	headers := http.Header{}
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		ProjectionMode:      OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly,
		InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
		InstallationEnabled: true, InstallationID: "77777777-7777-4777-8777-777777777777",
	})
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, "77777777-7777-4777-8777-777777777777", headers.Get("x-codex-installation-id"))
}

func TestApplyOpenAIOAuthIdentityPlanAlphaSearchCreatesCanonicalHeaderWithoutTouchingBody(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	const installationID = "77777777-7777-4777-8777-777777777777"
	body := []byte(" { \"id\" : \"native-alpha\", \"commands\" : [] } \n")
	headers := http.Header{
		"Session-Id":              {"client-session"},
		"Thread-Id":               {"client-thread"},
		"X-Client-Request-Id":     {"client-request"},
		"X-Codex-Installation-Id": {"client-installation"},
	}

	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		InstallationID: installationID, InstallationEnabled: true,
		ProjectionMode:     OpenAIOAuthIdentityProjectionAlphaSearch,
		InstallationPolicy: OpenAIOAuthInstallationAccountPin,
	})
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Empty(t, headers.Get("session-id"))
	require.Empty(t, headers.Get("thread-id"))
	require.Empty(t, headers.Get("x-client-request-id"))
	require.Empty(t, headers.Get(codexInstallationIDKey))

	values := headers.Values(openAIWSTurnMetadataHeader)
	require.Len(t, values, 1)
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(values[0]), &metadata))
	require.JSONEq(t, `"`+id.SessionID+`"`, string(metadata["session_id"]))
	require.JSONEq(t, `"`+id.ThreadID+`"`, string(metadata["thread_id"]))
	require.JSONEq(t, `"`+installationID+`"`, string(metadata[codexTurnMetadataInstallationIDKey]))
}

func TestApplyOpenAIOAuthIdentityPlanAlphaSearchRewritesObjectAndPreservesUnknownFields(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	const installationID = "88888888-8888-4888-8888-888888888888"
	body := []byte(`{"id":"native-alpha","commands":[{"type":"search"}]}`)
	headers := http.Header{openAIWSTurnMetadataHeader: {
		`{"session_id":"client-session","thread_id":"client-thread","installation_id":"client-installation","mcp_request_meta":{"request_id":"keep-me"},"unknown":7}`,
	}}

	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		InstallationID: installationID, InstallationEnabled: true,
		ProjectionMode:     OpenAIOAuthIdentityProjectionAlphaSearch,
		InstallationPolicy: OpenAIOAuthInstallationAccountPin,
	})
	require.NoError(t, err)
	require.Equal(t, body, out)
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(headers.Get(openAIWSTurnMetadataHeader)), &metadata))
	require.JSONEq(t, `"`+id.SessionID+`"`, string(metadata["session_id"]))
	require.JSONEq(t, `"`+id.ThreadID+`"`, string(metadata["thread_id"]))
	require.JSONEq(t, `"`+installationID+`"`, string(metadata[codexTurnMetadataInstallationIDKey]))
	require.JSONEq(t, `{"request_id":"keep-me"}`, string(metadata["mcp_request_meta"]))
	require.JSONEq(t, `7`, string(metadata["unknown"]))
}

func TestApplyOpenAIOAuthIdentityPlanAlphaSearchPreservesOpaqueValues(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	const installationID = "99999999-9999-4999-8999-999999999999"
	const firstOpaque = "  opaque-alpha\t"
	const secondOpaque = `["not-an-object"]`
	headers := http.Header{openAIWSTurnMetadataHeader: {firstOpaque, secondOpaque}}

	_, err = ApplyOpenAIOAuthIdentityPlan(headers, nil, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		InstallationID: installationID, InstallationEnabled: true,
		ProjectionMode:     OpenAIOAuthIdentityProjectionAlphaSearch,
		InstallationPolicy: OpenAIOAuthInstallationAccountPin,
	})
	require.NoError(t, err)
	values := headers.Values(openAIWSTurnMetadataHeader)
	require.Len(t, values, 2)
	require.Equal(t, firstOpaque, values[0])
	require.Equal(t, secondOpaque, values[1])
}

func TestApplyOpenAIOAuthIdentityPlanAlphaSearchHonorsComponentSwitches(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	const installationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

	tests := []struct {
		name                string
		turnEnabled         bool
		installationEnabled bool
		wantSession         bool
		wantInstallation    bool
	}{
		{name: "turn only", turnEnabled: true, wantSession: true},
		{name: "installation only", installationEnabled: true, wantInstallation: true},
		{name: "both disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			_, applyErr := ApplyOpenAIOAuthIdentityPlan(headers, nil, OpenAIOAuthIdentityPlan{
				TurnIdentity: id, TurnIdentityEnabled: tt.turnEnabled,
				InstallationID: installationID, InstallationEnabled: tt.installationEnabled,
				ProjectionMode:     OpenAIOAuthIdentityProjectionAlphaSearch,
				InstallationPolicy: OpenAIOAuthInstallationAccountPin,
			})
			require.NoError(t, applyErr)
			metadata := headers.Get(openAIWSTurnMetadataHeader)
			require.Equal(t, tt.wantSession, strings.Contains(metadata, id.SessionID))
			require.Equal(t, tt.wantInstallation, strings.Contains(metadata, installationID))
		})
	}
}

func TestApplyOpenAIOAuthIdentityPlanPreservesInvalidTurnMetadata(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	const invalidHeader = "  opaque-header\t"
	const validHeader = `{"session_id":"old","thread_id":"old"}`
	headers := http.Header{openAIWSTurnMetadataHeader: []string{invalidHeader, validHeader}}
	body := []byte(`{"model":"gpt","x-codex-turn-metadata":null,"client_metadata":{"keep":"value","x-codex-turn-metadata":"  opaque-body  "}}`)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	projectedHeaderValues := headers.Values(openAIWSTurnMetadataHeader)
	require.Len(t, projectedHeaderValues, 2)
	require.Equal(t, invalidHeader, projectedHeaderValues[0])
	require.Contains(t, projectedHeaderValues[1], id.SessionID)
	require.Equal(t, id.SessionID, headers.Get("session-id"))
	require.Equal(t, id.ThreadID, headers.Get("thread-id"))

	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &root))
	require.Equal(t, json.RawMessage(`null`), root[openAIWSTurnMetadataHeader])
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(root["client_metadata"], &metadata))
	require.JSONEq(t, `"  opaque-body  "`, string(metadata[openAIWSTurnMetadataHeader]))
	require.JSONEq(t, `"`+id.SessionID+`"`, string(metadata["session_id"]))
	require.JSONEq(t, `"`+id.ThreadID+`"`, string(metadata["thread_id"]))
}

func TestApplyOpenAIOAuthIdentityPlanPreservesOpaqueClientMetadataContainer(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	headers := http.Header{}
	body := []byte(`{"model":"gpt","client_metadata":"opaque-container","x-codex-turn-metadata":"{\"session_id\":\"old\",\"thread_id\":\"old\"}"}`)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
		InstallationEnabled: true,
		InstallationID:      "77777777-7777-4777-8777-777777777777",
	})
	require.NoError(t, err)
	require.Equal(t, id.SessionID, headers.Get("session-id"))
	require.Equal(t, id.ThreadID, headers.Get("thread-id"))
	require.Equal(t, "77777777-7777-4777-8777-777777777777", headers.Get("x-codex-installation-id"))

	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &root))
	require.JSONEq(t, `"opaque-container"`, string(root["client_metadata"]))
	var rewritten string
	require.NoError(t, json.Unmarshal(root[openAIWSTurnMetadataHeader], &rewritten))
	require.Contains(t, rewritten, id.SessionID)
}

func TestApplyOpenAIOAuthIdentityPlanInvalidTopLevelMetadataDoesNotCreateNestedCarrier(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	body := []byte(`{"model":"gpt","x-codex-turn-metadata":["opaque"],"client_metadata":{"keep":"value"}}`)
	out, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &root))
	require.Equal(t, json.RawMessage(`["opaque"]`), root[openAIWSTurnMetadataHeader])
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(root["client_metadata"], &metadata))
	require.NotContains(t, metadata, openAIWSTurnMetadataHeader)
	require.JSONEq(t, `"`+id.SessionID+`"`, string(metadata["session_id"]))
	require.JSONEq(t, `"`+id.ThreadID+`"`, string(metadata["thread_id"]))
}

func TestApplyOpenAIOAuthIdentityPlanExistingOnlyLeavesInvalidMetadataByteExact(t *testing.T) {
	id, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	const invalidHeader = "  opaque-alpha-header\t"
	headers := http.Header{openAIWSTurnMetadataHeader: []string{invalidHeader}}
	body := []byte(" { \"model\" : \"gpt\", \"x-codex-turn-metadata\" : \"  opaque-alpha-body  \" } \n")
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		TurnIdentity: id, TurnIdentityEnabled: true,
		ProjectionMode:     OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	})
	require.NoError(t, err)
	require.Equal(t, []string{invalidHeader}, headers.Values(openAIWSTurnMetadataHeader))
	require.Equal(t, body, out)
}

func TestApplyOpenAIOAuthIdentityPlanPreserveIsByteExactWithoutTurnProjection(t *testing.T) {
	body := []byte(" { \"client_metadata\" : { \"x-codex-installation-id\" : \"client\" } } \n")
	headers := http.Header{"X-Codex-Installation-Id": {"client"}}
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
		InstallationPolicy:  OpenAIOAuthInstallationPreserve,
		InstallationEnabled: true, InstallationID: "server",
	})
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, "client", headers.Get("X-Codex-Installation-Id"))
}

func TestApplyOpenAIOAuthIdentityPlanPreservesLargeJSONInteger(t *testing.T) {
	body := []byte(`{"sequence":9007199254740993,"client_metadata":{"installation_id":"client"}}`)
	out, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, OpenAIOAuthIdentityPlan{
		InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
		InstallationEnabled: true,
		InstallationID:      "77777777-7777-4777-8777-777777777777",
	})
	require.NoError(t, err)
	require.Contains(t, string(out), `"sequence":9007199254740993`)
}

func TestApplyOpenAIOAuthIdentityPlanCompactAlwaysStripsClientMetadata(t *testing.T) {
	body := []byte(`{"sequence":9007199254740993,"client_metadata":{"keep":"remove"}}`)
	out, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, OpenAIOAuthIdentityPlan{
		ProjectionMode:      OpenAIOAuthIdentityProjectionCompact,
		InstallationPolicy:  OpenAIOAuthInstallationAccountPin,
		InstallationEnabled: false,
	})
	require.NoError(t, err)
	require.NotContains(t, string(out), "client_metadata")
	require.Contains(t, string(out), `"sequence":9007199254740993`)
}

func TestApplyOpenAIOAuthIdentityPlanProjectsNormalizedClientIdentityLast(t *testing.T) {
	headers := http.Header{
		"Originator": {"opencode"},
		"User-Agent": {"luna/1.0.0"},
		"Version":    {"2.1.0"},
	}
	_, err := ApplyOpenAIOAuthIdentityPlan(headers, nil, OpenAIOAuthIdentityPlan{
		ClientIdentityEnabled: true,
		ClientIdentity: resolveCodexClientIdentityPlan(
			CodexClientIdentityNormalize,
			"codex_vscode/0.120.0 (Ubuntu 22.4.0; x86_64) vscode",
		),
	})
	require.NoError(t, err)
	require.Equal(t, openai.CodexDefaultOriginator, headers.Get("originator"))
	require.Equal(t, "codex-tui/"+codexCLIVersion+" (Ubuntu 22.4.0; x86_64) vscode (codex-tui; "+codexCLIVersion+")", headers.Get("user-agent"))
	require.Equal(t, codexCLIVersion, headers.Get("version"))
}

func TestApplyOpenAIOAuthIdentityPlanSafePairPreservesRecognizedClient(t *testing.T) {
	const clientUA = "codex-tui/0.145.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.145.2)"
	headers := http.Header{
		"Originator": {"wrong"},
		"User-Agent": {clientUA},
		"Version":    {"0.145.2"},
	}
	_, err := ApplyOpenAIOAuthIdentityPlan(headers, nil, OpenAIOAuthIdentityPlan{
		ClientIdentityEnabled: true,
		ClientIdentity:        resolveCodexClientIdentityPlan(CodexClientIdentitySafePair, ""),
	})
	require.NoError(t, err)
	require.Equal(t, "codex-tui", headers.Get("originator"))
	require.Equal(t, clientUA, headers.Get("user-agent"))
	require.Equal(t, "0.145.2", headers.Get("version"))
}

// ForceCodexCLI is an explicit gateway override, so it must still produce the
// runtime TUI triplet when fingerprint normalization (or only its client
// identity child) is disabled. Do not run this test in parallel: it replaces
// the process-wide canonical resolver.
func TestResolveOpenAIOAuthIdentityPlanForceCodexCLINormalizesWhenPolicyDisabled(t *testing.T) {
	const (
		resolvedUA = "codex_cli_rs/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
		wantUA     = "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)"
		clientUA   = "codex_vscode/0.145.2 (Mac OS X 14.0; arm64) vscode (codex_vscode; 0.145.2)"
	)
	SetCodexCanonicalUserAgentResolver(func() string { return resolvedUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	policies := map[string]CodexFingerprintPolicySnapshot{
		"master disabled": {},
		"client identity child disabled": {
			MasterEnabled: true, InstallationIDEnabled: true, TurnIdentityEnabled: true,
		},
	}
	for name, policy := range policies {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set(openAICodexFingerprintPolicyContextKey, policy)
			account := &Account{
				ID: 902, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"user_agent": "codex-tui/0.100.0 (Windows 11; x86_64) WindowsTerminal"},
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: true}}}
			plan, err := svc.ResolveOpenAIOAuthIdentityPlan(
				context.Background(), c, account, OpenAIOAuthIdentityCapture{}, OpenAIOAuthIdentityPlanOptions{
					ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
					InstallationPolicy: OpenAIOAuthInstallationPreserve,
				},
			)
			require.NoError(t, err)
			require.Equal(t, CodexClientIdentityNormalize, plan.ClientIdentity.Mode)
			require.Equal(t, wantUA, plan.ClientIdentity.UserAgent)
			require.Equal(t, openai.CodexDefaultOriginator, plan.ClientIdentity.Originator)
			require.Equal(t, "0.200.1", plan.ClientIdentity.Version)

			headers := http.Header{
				"Originator": {"codex_vscode"},
				"User-Agent": {clientUA},
				"Version":    {"0.145.2"},
			}
			_, err = ApplyOpenAIOAuthIdentityPlan(headers, nil, plan)
			require.NoError(t, err)
			require.Equal(t, wantUA, headers.Get("user-agent"))
			require.Equal(t, openai.CodexDefaultOriginator, headers.Get("originator"))
			require.Equal(t, "0.200.1", headers.Get("version"))
		})
	}
}

// Resolve owns the HTTP request snapshot. A version refresh after resolution
// must only affect the next request, never the final projection of this one.
// Do not run this test in parallel: it replaces the process-wide resolver.
func TestResolveOpenAIOAuthIdentityPlanFreezesHTTPClientIdentity(t *testing.T) {
	const (
		resolvedUA     = "codex_cli_rs/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
		wantResolvedUA = "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)"
		updatedUA      = "codex_cli_rs/0.201.2 (Mac OS X 15.1.0; arm64) iTerm.app"
	)
	SetCodexCanonicalUserAgentResolver(func() string { return resolvedUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{ID: 901, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	plan, err := (&OpenAIGatewayService{}).ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, account, OpenAIOAuthIdentityCapture{}, OpenAIOAuthIdentityPlanOptions{
			ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
			InstallationPolicy: OpenAIOAuthInstallationPreserve,
		},
	)
	require.NoError(t, err)
	require.Equal(t, wantResolvedUA, plan.ClientIdentity.UserAgent)
	require.Equal(t, openai.CodexDefaultOriginator, plan.ClientIdentity.Originator)
	require.Equal(t, "0.200.1", plan.ClientIdentity.Version)

	SetCodexCanonicalUserAgentResolver(func() string { return updatedUA })
	headers := http.Header{
		"Originator": {"opencode"},
		"User-Agent": {"luna/1.0.0"},
		"Version":    {"9.9.9"},
	}
	_, err = ApplyOpenAIOAuthIdentityPlan(headers, nil, plan)
	require.NoError(t, err)
	require.Equal(t, wantResolvedUA, headers.Get("user-agent"))
	require.Equal(t, openai.CodexDefaultOriginator, headers.Get("originator"))
	require.Equal(t, "0.200.1", headers.Get("version"))
}

// A lineage transition can rematerialize the turn plan on one HTTP-bridged WS
// connection. The canonical client triplet must remain the connection snapshot.
// Do not run this test in parallel: it replaces the process-wide resolver.
func TestResolveOpenAIOAuthIdentityPlanReusesWSConnectionClientIdentitySnapshot(t *testing.T) {
	const (
		resolvedUA     = "codex_cli_rs/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
		wantResolvedUA = "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)"
		updatedUA      = "codex_cli_rs/0.201.2 (Mac OS X 15.1.0; arm64) iTerm.app"
	)
	SetCodexCanonicalUserAgentResolver(func() string { return resolvedUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	account := &Account{ID: 903, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}
	options := OpenAIOAuthIdentityPlanOptions{
		ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	}

	first, err := svc.ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, account,
		CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"root"}}`), ""),
		options,
	)
	require.NoError(t, err)
	require.Equal(t, wantResolvedUA, first.ClientIdentity.UserAgent)

	SetCodexCanonicalUserAgentResolver(func() string { return updatedUA })
	second, err := svc.ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, account,
		CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"root","thread_id":"child"}}`), ""),
		options,
	)
	require.NoError(t, err)
	require.Equal(t, first.ClientIdentity, second.ClientIdentity)
}

// Invalid historical account UAs must be interpreted against the connection's
// canonical snapshot. Re-reading the hot global fallback here would let a
// lineage transition change the environment/version mid-connection.
// Do not run this test in parallel: it replaces the process-wide resolver.
func TestResolveOpenAIOAuthIdentityPlanInvalidStoredUAUsesFrozenConnectionFallback(t *testing.T) {
	const (
		resolvedUA     = "codex_cli_rs/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
		wantResolvedUA = "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)"
		updatedUA      = "codex_vscode/0.201.2 (Mac OS X 15.1.0; arm64) vscode"
	)
	SetCodexCanonicalUserAgentResolver(func() string { return resolvedUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	account := &Account{
		ID: 904, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"user_agent": "luna/1.0"},
	}
	svc := &OpenAIGatewayService{}
	options := OpenAIOAuthIdentityPlanOptions{
		ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	}

	first, err := svc.ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, account,
		CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"root"}}`), ""),
		options,
	)
	require.NoError(t, err)
	require.Equal(t, wantResolvedUA, first.ClientIdentity.UserAgent)

	SetCodexCanonicalUserAgentResolver(func() string { return updatedUA })
	second, err := svc.ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, account,
		CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"root","thread_id":"child"}}`), ""),
		options,
	)
	require.NoError(t, err)
	require.Equal(t, first.ClientIdentity, second.ClientIdentity)
}

// Credential failover rematerializes account-owned environment data,
// while the canonical version remains the immutable request snapshot.
// Do not run this test in parallel: it replaces the process-wide resolver.
func TestResolveOpenAIOAuthIdentityPlanFailoverUsesTargetAccountWithFrozenCanonicalVersion(t *testing.T) {
	const (
		resolvedUA = "codex_cli_rs/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
		updatedUA  = "codex_cli_rs/0.201.2 (Mac OS X 15.1.0; arm64) iTerm.app"
	)
	SetCodexCanonicalUserAgentResolver(func() string { return resolvedUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	firstAccount := &Account{
		ID: 905, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"user_agent": "codex_cli_rs/0.120.0 (Windows 11.0.26100; x86_64) WindowsTerminal",
		},
	}
	secondAccount := &Account{
		ID: 906, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"user_agent": "codex_vscode/0.130.0 (Mac OS X 14.0; arm64) vscode",
		},
	}
	options := OpenAIOAuthIdentityPlanOptions{
		ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	}
	svc := &OpenAIGatewayService{}

	first, err := svc.ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, firstAccount, OpenAIOAuthIdentityCapture{}, options,
	)
	require.NoError(t, err)
	require.Equal(t, "codex-tui/0.200.1 (Windows 11.0.26100; x86_64) WindowsTerminal (codex-tui; 0.200.1)", first.ClientIdentity.UserAgent)

	SetCodexCanonicalUserAgentResolver(func() string { return updatedUA })
	second, err := svc.ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, secondAccount, OpenAIOAuthIdentityCapture{}, options,
	)
	require.NoError(t, err)
	require.Equal(t, "codex-tui/0.200.1 (Mac OS X 14.0; arm64) vscode (codex-tui; 0.200.1)", second.ClientIdentity.UserAgent)
	require.Equal(t, openai.CodexDefaultOriginator, second.ClientIdentity.Originator)
	require.Equal(t, "0.200.1", second.ClientIdentity.Version)
	require.NotEqual(t, first.ClientIdentity, second.ClientIdentity)
}

func TestOpenAIOAuthIdentityPlanMatchesCredentialOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 78})

	firstAccount := &Account{ID: 907, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	secondAccount := &Account{ID: 908, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "plan-owner-match-secret"}}}
	options := OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true,
		ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:  OpenAIOAuthInstallationPreserve,
	}
	capture := CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"owner-match-root"}}`), "")
	plan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, firstAccount, capture, options)
	require.NoError(t, err)

	require.True(t, svc.OpenAIOAuthIdentityPlanMatches(context.Background(), c, firstAccount, plan, options))
	require.False(t, svc.OpenAIOAuthIdentityPlanMatches(context.Background(), c, secondAccount, plan, options))

	c.Set("api_key", &APIKey{ID: 79})
	require.False(t, svc.OpenAIOAuthIdentityPlanMatches(context.Background(), c, firstAccount, plan, options))
}

func TestGetOrResolveOpenAIOAuthOutboundIdentityUsesExactCaptureAndCredentialScope(t *testing.T) {
	newContext := func(apiKeyID int64) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set("api_key", &APIKey{ID: apiKeyID})
		return c
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "get-or-resolve-secret"}}}
	firstAccount := &Account{ID: 920, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	secondAccount := &Account{ID: 921, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	firstCapture := CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"first"}}`), "")
	secondCapture := CaptureOpenAIOAuthIdentity(nil, []byte(`{"client_metadata":{"session_id":"second"}}`), "")
	regular := OpenAIOAuthIdentityPlanOptions{
		ProjectionMode:     OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	}

	t.Run("exact match reuses cached plan", func(t *testing.T) {
		c := newContext(81)
		first, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, firstAccount, firstCapture, regular, nil)
		require.NoError(t, err)
		first.SocketDigest = "cached-marker"
		SetOpenAIOAuthIdentityPlan(c, first)
		second, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, firstAccount, firstCapture, regular, nil)
		require.NoError(t, err)
		require.Equal(t, "cached-marker", second.SocketDigest)
	})

	t.Run("validated pinned plan wins over context cache", func(t *testing.T) {
		c := newContext(81)
		pinned, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, firstAccount, firstCapture, regular, nil)
		require.NoError(t, err)
		cached := pinned
		cached.SocketDigest = "context-marker"
		SetOpenAIOAuthIdentityPlan(c, cached)
		pinned.SocketDigest = "pinned-marker"
		resolved, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, firstAccount, firstCapture, regular, &pinned)
		require.NoError(t, err)
		require.Equal(t, "pinned-marker", resolved.SocketDigest)
	})

	tests := []struct {
		name       string
		capture    OpenAIOAuthIdentityCapture
		account    *Account
		options    OpenAIOAuthIdentityPlanOptions
		mutate     func(*gin.Context)
		assertPlan func(*testing.T, OpenAIOAuthIdentityPlan)
	}{
		{
			name: "capture change", capture: secondCapture, account: firstAccount, options: regular,
			assertPlan: func(t *testing.T, plan OpenAIOAuthIdentityPlan) {
				require.Equal(t, secondCapture.Logical, plan.Capture.Logical)
			},
		},
		{
			name: "credential owner change", capture: firstCapture, account: secondAccount, options: regular,
			assertPlan: func(t *testing.T, plan OpenAIOAuthIdentityPlan) {
				require.NotEmpty(t, plan.CredentialOwnerNamespace)
			},
		},
		{
			name: "api key change", capture: firstCapture, account: firstAccount, options: regular,
			mutate: func(c *gin.Context) { c.Set("api_key", &APIKey{ID: 82}) },
			assertPlan: func(t *testing.T, plan OpenAIOAuthIdentityPlan) {
				require.Equal(t, int64(82), plan.APIKeyID)
			},
		},
		{
			name: "projection change", capture: firstCapture, account: firstAccount,
			options: OpenAIOAuthIdentityPlanOptions{
				ProjectionMode: OpenAIOAuthIdentityProjectionAlphaSearch, InstallationPolicy: OpenAIOAuthInstallationAccountPin,
			},
			assertPlan: func(t *testing.T, plan OpenAIOAuthIdentityPlan) {
				require.Equal(t, OpenAIOAuthIdentityProjectionAlphaSearch, plan.ProjectionMode)
				require.Equal(t, OpenAIOAuthInstallationAccountPin, plan.InstallationPolicy)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newContext(81)
			cached, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, firstAccount, firstCapture, regular, nil)
			require.NoError(t, err)
			cached.SocketDigest = "stale-marker"
			SetOpenAIOAuthIdentityPlan(c, cached)
			if tt.mutate != nil {
				tt.mutate(c)
			}
			resolved, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, tt.account, tt.capture, tt.options, nil)
			require.NoError(t, err)
			require.NotEqual(t, "stale-marker", resolved.SocketDigest)
			tt.assertPlan(t, resolved)
		})
	}
}

// SafePair still preserves recognized clients. Its canonical fallback is the
// only setting-derived part, and that fallback must share the plan snapshot.
// Do not run this test in parallel: it replaces the process-wide resolver.
func TestApplyOpenAIOAuthIdentityPlanSafePairFreezesFallback(t *testing.T) {
	const (
		resolvedUA     = "codex_cli_rs/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
		wantResolvedUA = "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)"
		updatedUA      = "codex_vscode/0.201.2 (Mac OS X 15.1.0; arm64) vscode"
	)
	SetCodexCanonicalUserAgentResolver(func() string { return resolvedUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })
	clientPlan := resolveCodexClientIdentityPlan(CodexClientIdentitySafePair, "")
	SetCodexCanonicalUserAgentResolver(func() string { return updatedUA })

	headers := http.Header{
		"Originator": {"opencode"},
		"User-Agent": {"luna/1.0.0"},
		"Version":    {"9.9.9"},
	}
	_, err := ApplyOpenAIOAuthIdentityPlan(headers, nil, OpenAIOAuthIdentityPlan{
		ClientIdentityEnabled: true,
		ClientIdentity:        clientPlan,
	})
	require.NoError(t, err)
	require.Equal(t, wantResolvedUA, headers.Get("user-agent"))
	require.Equal(t, openai.CodexDefaultOriginator, headers.Get("originator"))
	require.Equal(t, "0.200.1", headers.Get("version"))
}

// A WebSocket handshake reuses the plan stored on the connection context. A
// global version refresh while the connection is alive applies only to newly
// resolved connections.
// Do not run this test in parallel: it replaces the process-wide resolver.
func TestBuildOpenAIWSHeadersReusesFrozenClientIdentityPlan(t *testing.T) {
	const (
		resolvedUA     = "codex_cli_rs/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color"
		wantResolvedUA = "codex-tui/0.200.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.200.1)"
		updatedUA      = "codex_cli_rs/0.201.2 (Mac OS X 15.1.0; arm64) iTerm.app"
	)
	SetCodexCanonicalUserAgentResolver(func() string { return resolvedUA })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex-tui/0.145.2 (Mac OS X 14.0; arm64) iTerm")
	account := &Account{
		ID: 902, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
	}
	svc := &OpenAIGatewayService{}
	capture := CaptureOpenAIOAuthIdentity(c, nil, "")
	SetOpenAIOAuthIdentityCapture(c, capture)
	plan, err := svc.ResolveOpenAIOAuthIdentityPlan(
		context.Background(), c, account, capture, OpenAIOAuthIdentityPlanOptions{
			TurnIdentityEnabled: true,
			ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
			InstallationPolicy:  OpenAIOAuthInstallationPreserve,
		},
	)
	require.NoError(t, err)
	SetOpenAIOAuthIdentityPlan(c, plan)

	SetCodexCanonicalUserAgentResolver(func() string { return updatedUA })
	headers, resolution, err := svc.buildOpenAIWSHeadersWithBody(
		context.Background(), c, account, "oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true, "", "", "", nil, false, "gpt-5.1", "",
	)
	require.NoError(t, err)
	require.Equal(t, plan.ClientIdentity, resolution.OutboundIdentityPlan.ClientIdentity)
	require.Equal(t, wantResolvedUA, headers.Get("user-agent"))
	require.Equal(t, openai.CodexDefaultOriginator, headers.Get("originator"))
	require.Equal(t, "0.200.1", headers.Get("version"))
}

func TestApplyOpenAIOAuthIdentityPlanDoesNotProjectClientIdentityForLegacyPlan(t *testing.T) {
	headers := http.Header{
		"Originator": {"opencode"},
		"User-Agent": {"luna/1.0.0"},
		"Version":    {"2.1.0"},
	}
	_, err := ApplyOpenAIOAuthIdentityPlan(headers, nil, OpenAIOAuthIdentityPlan{})
	require.NoError(t, err)
	require.Equal(t, "opencode", headers.Get("originator"))
	require.Equal(t, "luna/1.0.0", headers.Get("user-agent"))
}

func TestLocalOpenAICodexSessionAliasesClaimReuseAndConflict(t *testing.T) {
	store := newOpenAICodexIdentityLocalStoreWithCapacity(32)
	first := uuid.Must(uuid.NewV7()).String()
	second := uuid.Must(uuid.NewV7()).String()
	claimed, err := store.GetOrCreateCodexSessionAliases(context.Background(), []string{"a", "b"}, first, time.Hour)
	require.NoError(t, err)
	require.Equal(t, first, claimed.Identity.SessionID)
	require.Equal(t, 2, claimed.AliasesClaimed)

	reused, err := store.GetOrCreateCodexSessionAliases(context.Background(), []string{"b", "c"}, second, time.Hour)
	require.NoError(t, err)
	require.True(t, reused.Reused)
	require.Equal(t, first, reused.Identity.SessionID)

	_, err = store.GetOrCreateCodexSession(context.Background(), "d", second, time.Hour)
	require.NoError(t, err)
	converged, err := store.GetOrCreateCodexSessionAliases(context.Background(), []string{"a", "d"}, first, time.Hour)
	require.NoError(t, err)
	require.Equal(t, first, converged.Identity.SessionID)
	require.Equal(t, 1, converged.ConflictsResolved)
	dWinner, err := store.GetOrCreateCodexSession(context.Background(), "d", second, time.Hour)
	require.NoError(t, err)
	require.Equal(t, first, dWinner)
}
