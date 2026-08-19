package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newOpenAIMemoryRoutingTestContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, recorder
}

func openAIMemoryRoutingTestPlan(t *testing.T) OpenAIOAuthIdentityPlan {
	t.Helper()
	plan := codexWireProjectionTestPlan(t)
	plan.WireProfile.RequestKind = CodexWireRequestMemory
	plan.Capture.WireProfile.RequestKind = CodexWireRequestMemory
	plan.RequestTurn = OpenAICodexRequestTurnSnapshot{}
	plan.Capture.RequestTurn = OpenAICodexRequestTurnSnapshot{}
	return plan
}

func TestOpenAICodexHTTPWireRequestKindRoutesOnlyBareResponsesMemory(t *testing.T) {
	plan := openAIMemoryRoutingTestPlan(t)

	for _, path := range []string{
		"/v1/responses",
		"/openai/v1/responses",
		"/responses",
		"/backend-api/codex/responses",
	} {
		t.Run(path, func(t *testing.T) {
			c, _ := newOpenAIMemoryRoutingTestContext(path)
			kind, err := openAICodexHTTPWireRequestKind(c, plan)
			require.NoError(t, err)
			require.Equal(t, CodexWireRequestMemory, kind)
		})
	}

	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/admin/accounts/1/test"} {
		t.Run(path, func(t *testing.T) {
			c, _ := newOpenAIMemoryRoutingTestContext(path)
			kind, err := openAICodexHTTPWireRequestKind(c, plan)
			require.NoError(t, err)
			require.Equal(t, CodexWireRequestTurn, kind)
		})
	}
}

func TestOpenAICodexHTTPWireRequestKindRejectsMemoryCompactionShapes(t *testing.T) {
	plan := openAIMemoryRoutingTestPlan(t)
	tests := []struct {
		name   string
		path   string
		mutate func(*gin.Context, *OpenAIOAuthIdentityPlan)
	}{
		{name: "legacy compact", path: "/v1/responses/compact"},
		{name: "native v2", path: "/v1/responses", mutate: func(c *gin.Context, _ *OpenAIOAuthIdentityPlan) {
			MarkOpenAINativeCompactionV2(c)
		}},
		{name: "compact projection", path: "/v1/responses", mutate: func(_ *gin.Context, plan *OpenAIOAuthIdentityPlan) {
			plan.ProjectionMode = OpenAIOAuthIdentityProjectionCompact
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, _ := newOpenAIMemoryRoutingTestContext(test.path)
			candidate := plan
			if test.mutate != nil {
				test.mutate(c, &candidate)
			}
			_, err := openAICodexHTTPWireRequestKind(c, candidate)
			require.ErrorIs(t, err, ErrOpenAICodexRequestKindConflict)
		})
	}
}

func TestOpenAICodexHTTPWireRequestKindIgnoresCapturedMemoryWhenTurnIdentityIsDisabled(t *testing.T) {
	base := openAIMemoryRoutingTestPlan(t)
	base.TurnIdentityRequested = false
	base.TurnIdentityEnabled = false
	base.TurnIdentity = OpenAICodexTurnIdentity{}

	tests := []struct {
		name       string
		path       string
		projection OpenAIOAuthIdentityProjectionMode
		markNative bool
		want       CodexWireRequestKind
	}{
		{name: "regular responses", path: "/v1/responses", projection: OpenAIOAuthIdentityProjectionRegular, want: CodexWireRequestTurn},
		{name: "passthrough responses", path: "/v1/responses", projection: OpenAIOAuthIdentityProjectionPassthrough, want: CodexWireRequestTurn},
		{name: "legacy compact", path: "/v1/responses/compact", projection: OpenAIOAuthIdentityProjectionCompact, want: CodexWireRequestCompaction},
		{name: "native compaction", path: "/v1/responses", projection: OpenAIOAuthIdentityProjectionRegular, markNative: true, want: CodexWireRequestCompaction},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, _ := newOpenAIMemoryRoutingTestContext(test.path)
			if test.markNative {
				MarkOpenAINativeCompactionV2(c)
			}
			plan := base
			plan.ProjectionMode = test.projection
			kind, err := openAICodexHTTPWireRequestKind(c, plan)
			require.NoError(t, err)
			require.Equal(t, test.want, kind)
		})
	}
}

func TestOpenAIGatewayMemoryConflictValidationIsDisabledWithTurnNormalization(t *testing.T) {
	body := codexWireTestBody(t, `{"request_kind":"memory","sandbox":"workspace-write"}`, nil)
	svc := newTransportIdentityTestService(t, false)
	for _, passthrough := range []bool{false, true} {
		name := "regular"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			c, _ := newOpenAIMemoryRoutingTestContext("/v1/responses/compact")
			SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentity(c, body, ""))
			account := &Account{ID: 99102, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			if passthrough {
				account.Extra = map[string]any{"openai_passthrough": true}
			}
			require.NoError(t, svc.validateOpenAICodexHTTPMemoryRequestShapeForAccount(context.Background(), c, account))
			require.False(t, svc.openAIOutboundSessionIdentityModeEnabledForAccount(context.Background(), c, account))
		})
	}
}

func TestCaptureOpenAIOAuthIdentityForCompatTurnOverridesSpoofedMemoryBeforeUUIDCapture(t *testing.T) {
	body := codexWireTestBody(t, `{"request_kind":"memory","sandbox":"workspace-write"}`, nil)
	for _, transport := range []string{"messages_compat", "chat_compat"} {
		t.Run(transport, func(t *testing.T) {
			capture := CaptureOpenAIOAuthIdentityForCompatTurn(nil, body, transport+"-session")
			require.Equal(t, CodexWireRequestTurn, capture.WireProfile.RequestKind)
			require.True(t, openAICodexRequestTurnSnapshotValid(capture.RequestTurn))
			require.True(t, capture.RequestTurn.Generated)
			require.Equal(t, capture.RequestTurn.ID, capture.WireProfile.TurnID.Value)

			plan := codexWireProjectionTestPlan(t)
			plan.Capture = capture
			plan.WireProfile = capture.WireProfile
			plan.RequestTurn = capture.RequestTurn
			finalPlan, err := FinalizeOpenAICodexWirePlanWithOptions(plan, FinalizeOpenAICodexWirePlanOptions{
				RequestKind: string(CodexWireRequestTurn),
			})
			require.NoError(t, err)
			out, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, finalPlan)
			require.NoError(t, err)
			nested := gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()
			require.Equal(t, "turn", gjson.Get(nested, "request_kind").String())
			require.Equal(t, capture.RequestTurn.ID, gjson.Get(nested, "turn_id").String())

			flagOff := OpenAIOAuthIdentityPlan{
				Capture: capture, WireProfile: capture.WireProfile, RequestTurn: capture.RequestTurn,
				ProjectionMode: OpenAIOAuthIdentityProjectionRegular,
			}
			unchanged, err := ApplyOpenAIOAuthIdentityPlan(http.Header{}, body, flagOff)
			require.NoError(t, err)
			require.Equal(t, body, unchanged)
		})
	}
}

func TestOpenAIGatewayForwardRejectsMemoryCompactionBeforeUpstreamWork(t *testing.T) {
	body := codexWireTestBody(t, `{"request_kind":"memory","sandbox":"workspace-write"}`, nil)
	account := &Account{ID: 99101, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	tests := []struct {
		name       string
		path       string
		markNative bool
	}{
		{name: "legacy compact", path: "/v1/responses/compact"},
		{name: "native v2", path: "/v1/responses", markNative: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, recorder := newOpenAIMemoryRoutingTestContext(test.path)
			if test.markNative {
				MarkOpenAINativeCompactionV2(c)
			}
			result, err := (&OpenAIGatewayService{}).Forward(context.Background(), c, account, body)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrOpenAICodexRequestKindConflict)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"type":"invalid_request_error"`)
			require.Contains(t, recorder.Body.String(), `"param":"client_metadata"`)
		})
	}
}

func TestOpenAIMemoryHeadersAreAllowedAcrossHTTPModes(t *testing.T) {
	for _, name := range []string{"x-openai-memgen-request", "x-openai-subagent"} {
		require.True(t, openaiAllowedHeaders[name], name)
		require.True(t, openaiPassthroughAllowedHeaders[name], name)
	}
}
