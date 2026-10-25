package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const codexNativeWakeupTurn = "c6d4c337-f88c-4e05-8328-2f4fa15901bc"

func TestCodexTurnUUIDv4KeepsStrictIDValidation(t *testing.T) {
	for _, kind := range []CodexWireRequestKind{"", CodexWireRequestTurn, CodexWireRequestCompaction} {
		id, valid := ResolveCodexTurnID(codexNativeWakeupTurn, kind)
		require.True(t, valid)
		require.Equal(t, CodexTurnIDUserUUIDv4, id.Kind)
		require.Equal(t, codexNativeWakeupTurn, id.Value)
		require.True(t, id.ValidFor(kind))
	}
	for _, value := range []string{
		strings.ToUpper(codexNativeWakeupTurn),
		"urn:uuid:" + codexNativeWakeupTurn,
		strings.ReplaceAll(codexNativeWakeupTurn, "-", ""),
		"c6d4c337-f88c-1e05-8328-2f4fa15901bc",
		"c6d4c337-f88c-4e05-0328-2f4fa15901bc",
		"opaque-user-turn",
		"00000000-0000-0000-0000-000000000000",
	} {
		_, valid := ResolveCodexTurnID(value, CodexWireRequestTurn)
		require.False(t, valid, value)
	}
	generated := newOpenAICodexRequestTurnSnapshot()
	require.Equal(t, CodexTurnIDUserUUIDv7, generated.TypedID.Kind)
	require.NoError(t, func() error { _, err := canonicalUUIDv7(generated.ID); return err }())
}

func codexUUIDv4TestGateway(t *testing.T) (*OpenAIGatewayService, *gin.Context, *Account, OpenAIOAuthIdentityPlanOptions) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 994})
	account := &Account{ID: 9194, Type: AccountTypeOAuth, Platform: PlatformOpenAI}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{JWT: config.JWTConfig{Secret: "native-turn-v4-test"}},
		accountRepo: newAuthorizedOpenAIOAuthTestRepo(account),
	}
	return svc, c, account, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: true, ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy: OpenAIOAuthInstallationPreserve,
	}
}

func TestCodexTurnUUIDv4HTTPResolveFinalizeAndRetry(t *testing.T) {
	for _, explicitTime := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit_time_%t", explicitTime), func(t *testing.T) {
			svc, c, account, options := codexUUIDv4TestGateway(t)
			metadata := map[string]any{
				"request_kind": "turn", "turn_id": codexNativeWakeupTurn,
				"parent_turn_id": codexNativeWakeupTurn, "root_turn_id": codexNativeWakeupTurn,
				"turn_trigger": "mailbox",
			}
			if explicitTime {
				metadata["turn_started_at_unix_ms"] = int64(1777777777123)
			}
			nested, err := json.Marshal(metadata)
			require.NoError(t, err)
			body := codexWireTestBody(t, string(nested), map[string]any{"session_id": "native-v4-session"})
			capture := CaptureOpenAIOAuthIdentity(c, body, "")
			require.NoError(t, capture.WireProfile.Validate())
			require.Equal(t, codexNativeWakeupTurn, capture.RequestTurn.ID)
			require.True(t, capture.RequestTurn.Explicit)
			require.False(t, capture.RequestTurn.Generated)
			if !explicitTime {
				require.Zero(t, capture.RequestTurn.StartedAtUnixMS, "UUIDv4 has no embedded timestamp")
			}
			plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, options, nil)
			require.NoError(t, err)
			for attempt := 0; attempt < 2; attempt++ {
				req := httptest.NewRequest(http.MethodPost, chatgptCodexURL, strings.NewReader(string(body)))
				out, err := svc.FinalizeOpenAIOAuthResponsesRequest(c, account, req, body, OpenAIOAuthResponsesFinalizeOptions{
					Plan: plan, FinalModel: "gpt-5.4", RequestKind: "turn", Transport: "test",
				})
				require.NoError(t, err)
				wire := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
				for _, field := range []string{"turn_id", "parent_turn_id", "root_turn_id"} {
					require.Equal(t, codexNativeWakeupTurn, gjson.Get(wire, field).String())
				}
				require.Equal(t, explicitTime, gjson.Get(wire, "turn_started_at_unix_ms").Exists())
				require.Equal(t, codexNativeWakeupTurn, gjson.Get(req.Header.Get(openAIWSTurnMetadataHeader), "turn_id").String())
			}
		})
	}
}

func TestCodexTurnUUIDv4WSContinuationKeepsTurnWithoutInventingTime(t *testing.T) {
	svc, c, account, options := codexUUIDv4TestGateway(t)
	options.ProjectionMode = OpenAIOAuthIdentityProjectionPassthrough
	firstBody := []byte(fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","client_metadata":{"session_id":"native-v4-ws","turn_id":"%s"},"input":"hi"}`, codexNativeWakeupTurn))
	firstCapture := CaptureOpenAIOAuthIdentity(c, firstBody, "")
	firstPlan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, firstCapture, options, nil)
	require.NoError(t, err)
	firstPlan, err = svc.finalizeOpenAIOAuthWSWirePlan(c, account, firstPlan, firstBody, openAIOAuthWSWireFinalizeOptions{})
	require.NoError(t, err)
	require.Equal(t, codexNativeWakeupTurn, firstPlan.RequestTurn.ID)
	require.Zero(t, firstPlan.RequestTurn.StartedAtUnixMS)

	for _, continuation := range []string{
		`{"type":"response.create","model":"gpt-5.4","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
		`{"type":"response.create","model":"gpt-5.4","input":[{"type":"compaction_trigger"}]}`,
	} {
		payload := []byte(continuation)
		capture := captureOpenAIWSFrameIdentity(payload, &firstPlan)
		require.Equal(t, codexNativeWakeupTurn, capture.RequestTurn.ID)
		plan, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture, options, &firstPlan)
		require.NoError(t, err)
		plan, err = svc.finalizeOpenAIOAuthWSWirePlan(c, account, plan, payload, openAIOAuthWSWireFinalizeOptions{})
		require.NoError(t, err)
		projected, err := svc.projectOpenAIOAuthWSFrame(c, account, plan, payload)
		require.NoError(t, err)
		wire := gjson.GetBytes(projected, "client_metadata.x-codex-turn-metadata").String()
		require.Equal(t, codexNativeWakeupTurn, gjson.Get(wire, "turn_id").String())
		require.False(t, gjson.Get(wire, "turn_started_at_unix_ms").Exists())
	}
}
