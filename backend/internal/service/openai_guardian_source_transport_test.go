package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func guardianSourceTransportFixture(t *testing.T) (*gin.Context, *Account, OpenAIOAuthIdentityPlan, *http.Request, []byte) {
	t.Helper()
	newID := func() string {
		id, err := uuid.NewV7()
		require.NoError(t, err)
		return id.String()
	}
	clientThread, session, thread, turn := newID(), newID(), newID(), newID()
	plan := OpenAIOAuthIdentityPlan{
		CredentialOwnerNamespace: t.Name(), APIKeyID: 71, TurnIdentityEnabled: true,
		Capture: OpenAIOAuthIdentityCapture{Logical: OpenAICodexLogicalTurnIdentity{
			SessionKey: clientThread, ThreadKey: clientThread, Relation: OpenAICodexTurnRelationRoot,
		}},
		RequestTurn: OpenAICodexRequestTurnSnapshot{ID: turn},
		TurnIdentity: OpenAICodexTurnIdentity{SessionID: session, ThreadID: thread,
			ParentThreadID: session, Relation: OpenAICodexTurnRelationDescendant},
	}
	nested, err := json.Marshal(map[string]any{"request_kind": "turn", "turn_id": turn})
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6", "input": "local fixture",
		"client_metadata": map[string]any{"session_id": session, "thread_id": thread, openAIWSTurnMetadataHeader: string(nested)},
	})
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, "https://example.test/responses", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("session-id", session)
	request.Header.Set("thread-id", thread)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIOAuthIdentityPlan(c, plan)
	t.Cleanup(func() {
		guardianSourceThreadBindings.Lock()
		defer guardianSourceThreadBindings.Unlock()
		for key := range guardianSourceThreadBindings.entries {
			if strings.HasPrefix(key, plan.CredentialOwnerNamespace+"\x00") {
				delete(guardianSourceThreadBindings.entries, key)
			}
		}
	})
	return c, &Account{ID: 82, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, plan, request, body
}

func guardianSourceTransportLookup(plan OpenAIOAuthIdentityPlan) string {
	logical := plan.Capture.Logical
	logical.GuardianClassifierSourceThreadKey = logical.ThreadKey
	logical.GuardianClassifierParentTurnKey = plan.RequestTurn.ID
	return lookupOpenAICodexGuardianSourceThread(plan.CredentialOwnerNamespace, plan.APIKeyID, plan.TurnIdentity.SessionID, logical)
}

func TestGuardianSourceHTTPBindsAtSendWithObservationAndTelemetryDisabled(t *testing.T) {
	previous := IsFingerprintObservationEnabled()
	SetFingerprintObservationEnabled(false)
	t.Cleanup(func() { SetFingerprintObservationEnabled(previous) })
	c, account, plan, request, body := guardianSourceTransportFixture(t)
	request = markOpenAIGuardianSourceHTTPRequest(request, c, account)
	require.Empty(t, guardianSourceTransportLookup(plan), "construction is not a physical send")
	ClearOpenAIOAuthIdentityPlan(c)
	upstream := &pluginRoutingHTTPUpstream{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	response, err := svc.doOpenAIUpstream(request, "", account)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, 1, upstream.doCalls)
	require.Equal(t, plan.TurnIdentity.ThreadID, guardianSourceTransportLookup(plan))
	remaining, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.Equal(t, body, remaining, "source tracking must not consume the inference body")
}

func TestGuardianSourceHTTPRejectsUnsentOrChangedIdentity(t *testing.T) {
	for _, name := range []string{"unmarked", "changed_thread", "conflicting_alias", "missing_body_snapshot", "different_account"} {
		t.Run(name, func(t *testing.T) {
			c, account, plan, request, _ := guardianSourceTransportFixture(t)
			if name != "unmarked" {
				request = markOpenAIGuardianSourceHTTPRequest(request, c, account)
			}
			switch name {
			case "changed_thread":
				id, err := uuid.NewV7()
				require.NoError(t, err)
				request.Header.Set("thread-id", id.String())
			case "conflicting_alias":
				id, err := uuid.NewV7()
				require.NoError(t, err)
				request.Header.Set("thread_id", id.String())
			case "missing_body_snapshot":
				request.GetBody = nil
			case "different_account":
				account.ID++
			}
			svc := &OpenAIGatewayService{httpUpstream: &pluginRoutingHTTPUpstream{}}
			response, err := svc.doOpenAIUpstream(request, "", account)
			require.NoError(t, err)
			t.Cleanup(func() { _ = response.Body.Close() })
			require.Empty(t, guardianSourceTransportLookup(plan))
		})
	}
}
