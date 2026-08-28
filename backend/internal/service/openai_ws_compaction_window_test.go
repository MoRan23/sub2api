package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newOpenAICodexWSCompactWindowTestPlan(t *testing.T, secret string, apiKeyID int64) (*OpenAIGatewayService, *Account, OpenAIOAuthIdentityPlan) {
	t.Helper()
	threadID, err := uuid.NewV7()
	require.NoError(t, err)
	turnID, err := uuid.NewV7()
	require.NoError(t, err)
	contextWindowID, err := uuid.NewV7()
	require.NoError(t, err)
	mappingKey, err := OpenAICodexWindowMappingKey(secret, "account:4242", apiKeyID, threadID.String())
	require.NoError(t, err)

	plan := OpenAIOAuthIdentityPlan{
		RequestTurn: OpenAICodexRequestTurnSnapshot{
			ID:              turnID.String(),
			TypedID:         CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: turnID.String()},
			StartedAtUnixMS: time.Now().UnixMilli(),
		},
		WireProfile: CodexWireProfile{
			Revision:    CodexWireProfileRevision,
			Commit:      CodexWireProfileCommit,
			Finalized:   true,
			RequestKind: CodexWireRequestCompaction,
			Compaction: marshalCodexCompactionMetadata(
				DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationRemoteV2),
			),
			CompactionMode: CodexCompactionModeRemoteV2,
			TurnID:         CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: turnID.String()},
		},
		TurnIdentity: OpenAICodexTurnIdentity{
			SessionID: threadID.String(),
			ThreadID:  threadID.String(),
			Relation:  OpenAICodexTurnRelationRoot,
		},
		TurnIdentityRequested:    true,
		TurnIdentityEnabled:      true,
		CredentialOwnerNamespace: "account:4242",
		APIKeyID:                 apiKeyID,
	}
	plan, err = BindOpenAICodexWindowToPlan(plan, OpenAICodexWindowSnapshot{
		ThreadID:        threadID.String(),
		ContextWindowID: contextWindowID.String(),
	}, mappingKey)
	require.NoError(t, err)
	return &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: secret}}},
		&Account{ID: 4242, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, plan
}

func TestObserveOpenAICodexWSCompactionDeliveryRequiresDeliveredSuccessfulTerminal(t *testing.T) {
	compactItem := []byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction"}}`)

	tests := []struct {
		name      string
		doneCount int
		terminal  []byte
		valid     bool
	}{
		{name: "completed", doneCount: 1, terminal: []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_1","type":"compaction"}]}}`), valid: true},
		{name: "completed ignores different terminal item", doneCount: 1, terminal: []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_terminal","type":"compaction"}]}}`), valid: true},
		{name: "completed without done", terminal: []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_1","type":"compaction"}]}}`)},
		{name: "response done is not completed", doneCount: 1, terminal: []byte(`{"type":"response.done","response":{"status":"done","output":[{"id":"cmp_1","type":"compaction"}]}}`)},
		{name: "failed event", doneCount: 1, terminal: []byte(`{"type":"response.failed","response":{"status":"failed"}}`)},
		{name: "completed envelope with incomplete status", doneCount: 1, terminal: []byte(`{"type":"response.completed","response":{"status":"incomplete","output":[{"id":"cmp_1","type":"compaction"}]}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delivery := &openAICodexWSCompactionDelivery{mode: CodexCompactionModeRemoteV2}
			for i := 0; i < tt.doneCount; i++ {
				observeOpenAICodexWSCompactionDelivery(delivery, compactItem)
			}
			observeOpenAICodexWSCompactionDelivery(delivery, tt.terminal)
			require.Equal(t, tt.valid, delivery.Valid())
		})
	}
}

func TestObserveOpenAICodexWSCompactionDeliveryRejectsRepeatedRemoteV2Done(t *testing.T) {
	delivery := &openAICodexWSCompactionDelivery{mode: CodexCompactionModeRemoteV2}
	done := []byte(`{"type":"response.output_item.done","item":{"id":"cmp_replayed","type":"compaction","encrypted_content":"same"}}`)
	observeOpenAICodexWSCompactionDelivery(delivery, done)
	observeOpenAICodexWSCompactionDelivery(delivery, done)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_replayed","type":"compaction","encrypted_content":"same"}]}}`))

	require.False(t, delivery.Valid())
}

func TestObserveOpenAICodexWSLocalCompactionRequiresDeliveredCompleted(t *testing.T) {
	message := []byte(`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","status":"completed"}}`)
	tests := []struct {
		name   string
		events [][]byte
		valid  bool
	}{
		{
			name: "message and completed",
			events: [][]byte{
				message,
				[]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`),
			},
			valid: true,
		},
		{
			name:   "completed with empty output",
			events: [][]byte{[]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)},
			valid:  true,
		},
		{
			name: "response done is not completed",
			events: [][]byte{
				message,
				[]byte(`{"type":"response.done","response":{"status":"done","output":[]}}`),
			},
		},
		{
			name: "failed after message",
			events: [][]byte{
				message,
				[]byte(`{"type":"response.failed","response":{"status":"failed"}}`),
			},
		},
		{
			name: "completed event with done status",
			events: [][]byte{
				message,
				[]byte(`{"type":"response.completed","response":{"status":"done","output":[]}}`),
			},
		},
		{
			name:   "completed output carries message",
			events: [][]byte{[]byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"msg_1","type":"message","status":"completed"}]}}`)},
			valid:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delivery := &openAICodexWSCompactionDelivery{mode: CodexCompactionModeLocalResponses}
			for _, event := range tt.events {
				observeOpenAICodexWSCompactionDelivery(delivery, event)
			}
			require.Equal(t, tt.valid, delivery.Valid())
		})
	}
}

func TestOpenAICodexWSCompactionDeliveryRequiresFinalizedMatchingMode(t *testing.T) {
	_, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-mode-gate-secret", 7418)
	require.NotNil(t, openAICodexWSCompactionDeliveryForPlan(account, plan))

	tests := []struct {
		name   string
		mutate func(*OpenAIOAuthIdentityPlan)
	}{
		{name: "not finalized", mutate: func(plan *OpenAIOAuthIdentityPlan) { plan.WireProfile.Finalized = false }},
		{name: "mode missing", mutate: func(plan *OpenAIOAuthIdentityPlan) { plan.WireProfile.CompactionMode = CodexCompactionModeNone }},
		{name: "mode metadata mismatch", mutate: func(plan *OpenAIOAuthIdentityPlan) {
			plan.WireProfile.CompactionMode = CodexCompactionModeLocalResponses
		}},
		{name: "request kind only", mutate: func(plan *OpenAIOAuthIdentityPlan) {
			plan.WireProfile.Compaction = nil
			plan.WireProfile.CompactionMode = CodexCompactionModeNone
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := plan
			tt.mutate(&invalid)
			require.Nil(t, openAICodexWSCompactionDeliveryForPlan(account, invalid))
		})
	}
}

func TestCommitOpenAICodexWSCompactionAfterDeliveryAdvancesPlanAndRebindsTurnState(t *testing.T) {
	const (
		secret   = "ws-compact-window-test-secret"
		apiKeyID = int64(7419)
		state    = "opaque-delivered-state"
	)
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, secret, apiKeyID)
	initialContextWindowID := plan.Window.ContextWindowID
	delivery := openAICodexWSCompactionDeliveryForPlan(account, plan)
	require.NotNil(t, delivery)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction"}}`))
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_1","type":"compaction"}]}}`))
	require.True(t, delivery.Valid())

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), c, account, &plan, delivery, state))
	require.Equal(t, uint64(1), plan.Window.Number)
	require.Equal(t, plan.Window.WindowID(), plan.WireProfile.WindowID)
	require.NotEqual(t, initialContextWindowID, plan.Window.ContextWindowID)
	require.Equal(t, plan.Window.ContextWindowID, plan.WireProfile.ContextWindowID)
	_, err := canonicalUUIDv7(plan.Window.ContextWindowID)
	require.NoError(t, err)

	contextPlan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.Equal(t, plan.Window, contextPlan.Window)

	stateKey, err := OpenAICodexTurnStateProvenanceKey(secret, apiKeyID, state)
	require.NoError(t, err)
	origin, err := processOpenAICodexTurnStateOriginStore.GetOpenAICodexTurnStateOrigin(context.Background(), stateKey)
	require.NoError(t, err)
	require.Equal(t, OpenAICodexTurnStateIdentityDigest(plan), origin.TurnIdentityDigest)
	require.NoError(t, processOpenAICodexTurnStateOriginStore.DeleteOpenAICodexTurnStateOrigin(context.Background(), stateKey))
}

func TestCommitOpenAICodexWSCompactionAfterDeliveryDoesNotAdvanceIncompleteDelivery(t *testing.T) {
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-incomplete-window-test-secret", 7420)
	initialContextWindowID := plan.Window.ContextWindowID
	delivery := openAICodexWSCompactionDeliveryForPlan(account, plan)
	require.NotNil(t, delivery)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction"}}`))

	require.False(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &plan, delivery, ""))
	require.Zero(t, plan.Window.Number)
	require.Equal(t, initialContextWindowID, plan.Window.ContextWindowID)
}

func TestCommitOpenAICodexWSCompactionAfterDeliveryDoesNotAdvanceRepeatedRemoteV2Done(t *testing.T) {
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-replayed-done-window-test-secret", 7427)
	initialWindow := plan.Window
	delivery := openAICodexWSCompactionDeliveryForPlan(account, plan)
	require.NotNil(t, delivery)
	done := []byte(`{"type":"response.output_item.done","item":{"id":"cmp_replayed","type":"compaction","encrypted_content":"same"}}`)
	observeOpenAICodexWSCompactionDelivery(delivery, done)
	observeOpenAICodexWSCompactionDelivery(delivery, done)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_replayed","type":"compaction","encrypted_content":"same"}]}}`))

	require.False(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &plan, delivery, ""))
	require.Equal(t, initialWindow, plan.Window)
}

func TestCommitOpenAICodexWSCompactionAfterDeliveryDuplicateCommitKeepsStoredContextWindowWinner(t *testing.T) {
	svc, account, originalPlan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-duplicate-window-test-secret", 7421)
	newDelivery := func() *openAICodexWSCompactionDelivery {
		delivery := openAICodexWSCompactionDeliveryForPlan(account, originalPlan)
		require.NotNil(t, delivery)
		observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.output_item.done","item":{"id":"cmp_duplicate","type":"compaction"}}`))
		observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_duplicate","type":"compaction"}]}}`))
		require.True(t, delivery.Valid())
		return delivery
	}

	firstPlan := originalPlan
	secondPlan := originalPlan
	require.True(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &firstPlan, newDelivery(), ""))
	require.True(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &secondPlan, newDelivery(), ""))
	require.Equal(t, uint64(1), firstPlan.Window.Number)
	require.Equal(t, firstPlan.Window, secondPlan.Window)
	require.NotEqual(t, originalPlan.Window.ContextWindowID, firstPlan.Window.ContextWindowID)
}

func TestCommitOpenAICodexWSCompactionAfterDeliverySameDeliveryIsOneShot(t *testing.T) {
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-one-shot-window-test-secret", 7425)
	delivery := openAICodexWSCompactionDeliveryForPlan(account, plan)
	require.NotNil(t, delivery)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.output_item.done","item":{"id":"cmp_one_shot","type":"compaction"}}`))
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_one_shot","type":"compaction"}]}}`))

	require.True(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &plan, delivery, ""))
	winner := plan.Window
	require.Equal(t, uint64(1), winner.Number)
	require.False(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &plan, delivery, ""))
	require.Equal(t, winner, plan.Window)
}

func TestCommitOpenAICodexWSCompactionAfterDeliveryUsesFrozenModeAcrossPlanMutation(t *testing.T) {
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-frozen-mode-window-test-secret", 7426)
	delivery := openAICodexWSCompactionDeliveryForPlan(account, plan)
	require.NotNil(t, delivery)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.output_item.done","item":{"id":"cmp_frozen_mode","type":"compaction"}}`))
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_frozen_mode","type":"compaction"}]}}`))

	plan.WireProfile.CompactionMode = CodexCompactionModeLocalResponses
	plan.WireProfile.Compaction = marshalCodexCompactionMetadata(
		DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationResponses),
	)
	require.True(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &plan, delivery, ""))
	require.Equal(t, uint64(1), plan.Window.Number)
	require.Equal(t, CodexCompactionModeLocalResponses, plan.WireProfile.CompactionMode, "current plan only receives the stored window winner")
}

func TestOpenAICodexWSCompactionDigestBindsCurrentContextWindow(t *testing.T) {
	const (
		secret   = "ws-context-digest-test-secret"
		apiKeyID = int64(7422)
	)
	_, _, plan := newOpenAICodexWSCompactWindowTestPlan(t, secret, apiKeyID)
	first, err := OpenAICodexCompactTurnDigest(
		secret,
		plan.CredentialOwnerNamespace,
		plan.APIKeyID,
		plan.Window,
		plan.RequestTurn.ID,
	)
	require.NoError(t, err)

	otherContextWindowID, err := uuid.NewV7()
	require.NoError(t, err)
	conflictingWindow := plan.Window
	conflictingWindow.ContextWindowID = otherContextWindowID.String()
	second, err := OpenAICodexCompactTurnDigest(
		secret,
		plan.CredentialOwnerNamespace,
		plan.APIKeyID,
		conflictingWindow,
		plan.RequestTurn.ID,
	)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}

func TestCommitOpenAICodexWSCompactionAfterDeliverySameNumberDifferentContextRebindsStoredWinner(t *testing.T) {
	const (
		secret   = "ws-stale-window-test-secret"
		apiKeyID = int64(7423)
	)
	svc, account, stalePlan := newOpenAICodexWSCompactWindowTestPlan(t, secret, apiKeyID)
	staleContextWindowID := stalePlan.Window.ContextWindowID
	winnerContextWindowID, err := uuid.NewV7()
	require.NoError(t, err)
	winner, err := svc.ResolveOpenAICodexWindowSnapshot(
		context.Background(),
		stalePlan.WindowMappingKey,
		stalePlan.Window.ThreadID,
		winnerContextWindowID.String(),
	)
	require.NoError(t, err)
	require.Equal(t, stalePlan.Window.Number, winner.Number)
	require.Equal(t, winnerContextWindowID.String(), winner.ContextWindowID)
	require.NotEqual(t, staleContextWindowID, winner.ContextWindowID)

	delivery := openAICodexWSCompactionDeliveryForPlan(account, stalePlan)
	require.NotNil(t, delivery)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.output_item.done","item":{"id":"cmp_stale","type":"compaction"}}`))
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_stale","type":"compaction"}]}}`))
	require.True(t, delivery.Valid())

	require.True(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &stalePlan, delivery, ""))
	require.Equal(t, winner, stalePlan.Window)
	require.Equal(t, winner.ContextWindowID, stalePlan.WireProfile.ContextWindowID)
}

func TestOpenAIWSHTTPBridgeLocalCompactionRetryKeepsWindowAndCommitsOnce(t *testing.T) {
	const apiKeyID = int64(7424)
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-http-bridge-local-secret", apiKeyID)
	plan.WireProfile.CompactionMode = CodexCompactionModeLocalResponses
	plan.WireProfile.Compaction = marshalCodexCompactionMetadata(
		DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationResponses),
	)
	plan.Capture.WireProfile = cloneCodexWireProfile(plan.WireProfile)
	plan.Capture.WireProfile.Finalized = false
	plan.Capture.RequestTurn = plan.RequestTurn
	plan.Capture.ContextWindowIDCandidate = plan.Window.ContextWindowID
	plan.Capture.Logical = OpenAICodexLogicalTurnIdentity{
		SessionKey: plan.TurnIdentity.SessionID,
		ThreadKey:  plan.TurnIdentity.ThreadID,
		Relation:   OpenAICodexTurnRelationRoot,
		Source:     OpenAIOutboundSessionLogicalKeySourceTurnMetadata,
		Explicit:   true,
	}
	require.False(t, openAIWSHTTPBridgeUsesNativeCompactionV2(plan))
	remotePlan := plan
	remotePlan.WireProfile.CompactionMode = CodexCompactionModeRemoteV2
	remotePlan.WireProfile.Compaction = marshalCodexCompactionMetadata(
		DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationRemoteV2),
	)
	require.True(t, openAIWSHTTPBridgeUsesNativeCompactionV2(remotePlan))

	windowCache := &passthroughCompactWindowCache{}
	svc.cache = windowCache
	seeded, err := svc.ResolveOpenAICodexWindowSnapshot(
		context.Background(),
		plan.WindowMappingKey,
		plan.Window.ThreadID,
		plan.Window.ContextWindowID,
	)
	require.NoError(t, err)
	plan, err = BindOpenAICodexWindowToPlan(plan, seeded, plan.WindowMappingKey)
	require.NoError(t, err)
	requestContextWindowID := plan.Window.ContextWindowID

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens.","param":"max_output_tokens"}}`,
			)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid-ws-http-bridge-local"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_http_bridge_local\",\"status\":\"completed\",\"model\":\"gpt-5.1\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}}
	svc.httpUpstream = upstream
	svc.toolCorrector = NewCodexToolCorrector()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.150.1")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &APIKey{ID: apiKeyID})

	localMetadata := `{"request_kind":"compaction","compaction":{"trigger":"manual","reason":"user_requested","implementation":"responses","phase":"standalone_turn","strategy":"memento"}}`
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"stream":true,
		"max_output_tokens":64,
		"input":[{"role":"user","content":"compact locally"}],
		"client_metadata":{"x-codex-turn-metadata":` + strconv.Quote(localMetadata) + `}
	}`)
	var delivered [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(),
		c,
		account,
		"oauth-token",
		payload,
		len(payload),
		"gpt-5.1",
		"",
		"",
		"",
		"",
		1,
		func(message []byte) error {
			delivered = append(delivered, append([]byte(nil), message...))
			return nil
		},
		&plan,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_ws_http_bridge_local", result.RequestID)
	require.Len(t, delivered, 1)
	require.Equal(t, "response.completed", gjson.GetBytes(delivered[0], "type").String())
	require.Empty(t, gjson.GetBytes(delivered[0], "response.output").Array())

	require.Len(t, upstream.bodies, 2)
	var requestTurnID string
	for index, body := range upstream.bodies {
		metadata := gjson.GetBytes(body, "client_metadata."+openAIWSTurnMetadataHeader).String()
		require.Equal(t, "compaction", gjson.Get(metadata, "request_kind").String())
		require.Equal(t, CodexCompactionImplementationResponses, gjson.Get(metadata, "compaction.implementation").String())
		require.Equal(t, requestContextWindowID, gjson.Get(metadata, "context_window_id").String())
		turnID := gjson.Get(metadata, "turn_id").String()
		if index == 0 {
			requestTurnID = turnID
			require.True(t, gjson.GetBytes(body, "max_output_tokens").Exists())
		} else {
			require.Equal(t, requestTurnID, turnID)
			require.False(t, gjson.GetBytes(body, "max_output_tokens").Exists())
		}
		require.False(t, HasCompactionTriggerInInput(body))
	}

	windowCache.mu.Lock()
	commitCalls := windowCache.commitCalls
	commitResult := windowCache.lastResult
	windowCache.mu.Unlock()
	require.Equal(t, 1, commitCalls)
	require.Equal(t, uint64(1), commitResult.Snapshot.Number)
	require.NotEqual(t, requestContextWindowID, commitResult.Snapshot.ContextWindowID)
	require.Equal(t, commitResult.Snapshot, plan.Window)
}

func TestOpenAIWSHTTPBridgeLocalCompactionClientDisconnectDoesNotCommit(t *testing.T) {
	const apiKeyID = int64(7425)
	svc, account, plan := newOpenAICodexWSCompactWindowTestPlan(t, "ws-http-bridge-local-disconnect-secret", apiKeyID)
	plan.WireProfile.CompactionMode = CodexCompactionModeLocalResponses
	plan.WireProfile.Compaction = marshalCodexCompactionMetadata(
		DefaultCodexCompactionTurnMetadata(CodexCompactionImplementationResponses),
	)
	plan.Capture.WireProfile = cloneCodexWireProfile(plan.WireProfile)
	plan.Capture.WireProfile.Finalized = false
	plan.Capture.RequestTurn = plan.RequestTurn
	plan.Capture.ContextWindowIDCandidate = plan.Window.ContextWindowID
	plan.Capture.Logical = OpenAICodexLogicalTurnIdentity{
		SessionKey: plan.TurnIdentity.SessionID,
		ThreadKey:  plan.TurnIdentity.ThreadID,
		Relation:   OpenAICodexTurnRelationRoot,
		Source:     OpenAIOutboundSessionLogicalKeySourceTurnMetadata,
		Explicit:   true,
	}

	windowCache := &passthroughCompactWindowCache{}
	svc.cache = windowCache
	seeded, err := svc.ResolveOpenAICodexWindowSnapshot(
		context.Background(),
		plan.WindowMappingKey,
		plan.Window.ThreadID,
		plan.Window.ContextWindowID,
	)
	require.NoError(t, err)
	plan, err = BindOpenAICodexWindowToPlan(plan, seeded, plan.WindowMappingKey)
	require.NoError(t, err)
	requestWindow := plan.Window

	svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid-ws-http-bridge-local-disconnect"},
		},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_http_bridge_local_disconnect\",\"status\":\"completed\",\"model\":\"gpt-5.1\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc.toolCorrector = NewCodexToolCorrector()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.150.1")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &APIKey{ID: apiKeyID})

	localMetadata := `{"request_kind":"compaction","compaction":{"trigger":"manual","reason":"user_requested","implementation":"responses","phase":"standalone_turn","strategy":"memento"}}`
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"stream":true,
		"input":[{"role":"user","content":"compact locally"}],
		"client_metadata":{"x-codex-turn-metadata":` + strconv.Quote(localMetadata) + `}
	}`)
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(),
		c,
		account,
		"oauth-token",
		payload,
		len(payload),
		"gpt-5.1",
		"",
		"",
		"",
		"",
		1,
		func([]byte) error { return io.EOF },
		&plan,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_ws_http_bridge_local_disconnect", result.RequestID)
	require.Equal(t, "response.completed", result.UpstreamTerminalEvent)

	windowCache.mu.Lock()
	commitCalls := windowCache.commitCalls
	windowCache.mu.Unlock()
	require.Zero(t, commitCalls)
	require.Equal(t, requestWindow, plan.Window, "client disconnect before delivery must retain the physical request window")
}

func TestOpenAIHTTPToUpstreamWSLocalCompactionDownstreamWriteFailureDoesNotCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.JWT.Secret = "http-to-ws-local-compaction-write-failure-secret"
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_http_to_ws_local_write_failure","status":"completed","model":"gpt-5.1","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`),
	}}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)
	defer pool.Close()
	windowCache := &passthroughCompactWindowCache{}
	svc := &OpenAIGatewayService{
		cfg: cfg,
		settingService: NewSettingService(&openAIUUIDv7RuntimeRepo{values: map[string]string{
			SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
		}}, nil),
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            windowCache,
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          4243,
		Name:        "http-to-ws-local-compaction-write-failure",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token"},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
			openAIPinnedInstallationIDKey:     transportTestPinnedInstallationID,
		},
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.150.1")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &APIKey{ID: 7426})
	c.Writer = &committedErrorOpenAIWSGinWriter{ResponseWriter: c.Writer}

	localMetadata := `{"session_id":"01989f44-7c00-7000-8000-000000000301","thread_id":"01989f44-7c00-7000-8000-000000000301","turn_id":"01989f44-7c00-7000-8000-000000000302","turn_started_at_unix_ms":1,"request_kind":"compaction","compaction":{"trigger":"manual","reason":"user_requested","implementation":"responses","phase":"standalone_turn","strategy":"memento"}}`
	body := []byte(`{
		"model":"gpt-5.1",
		"stream":true,
		"input":[{"role":"user","content":"compact locally"}],
		"client_metadata":{"x-codex-turn-metadata":` + strconv.Quote(localMetadata) + `}
	}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_http_to_ws_local_write_failure", result.RequestID)
	require.Equal(t, "response.completed", result.UpstreamTerminalEvent)

	upstreamConn.mu.Lock()
	require.Len(t, upstreamConn.writes, 1)
	forwarded := requestToJSONString(upstreamConn.writes[0])
	upstreamConn.mu.Unlock()
	metadata := gjson.Get(forwarded, "client_metadata."+openAIWSTurnMetadataHeader).String()
	require.Equal(t, "compaction", gjson.Get(metadata, "request_kind").String())
	require.Equal(t, CodexCompactionImplementationResponses, gjson.Get(metadata, "compaction.implementation").String())
	requestContextWindowID := gjson.Get(metadata, "context_window_id").String()
	_, err = canonicalUUIDv7(requestContextWindowID)
	require.NoError(t, err)

	windowCache.mu.Lock()
	commitCalls := windowCache.commitCalls
	windowCache.mu.Unlock()
	require.Zero(t, commitCalls)
	contextPlan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	require.Zero(t, contextPlan.Window.Number)
	require.Equal(t, requestContextWindowID, contextPlan.Window.ContextWindowID)
}

func TestOpenAIHTTPToUpstreamWSRemoteV2NonStreamingDoneCardinalityControlsCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name         string
		doneCount    int
		expectCommit int
	}{
		{name: "terminal output without done is rejected", doneCount: 0},
		{name: "single done commits", doneCount: 1, expectCommit: 1},
		{name: "same done twice is rejected", doneCount: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			threadID, err := uuid.NewV7()
			require.NoError(t, err)
			turnID, err := uuid.NewV7()
			require.NoError(t, err)
			cfg := &config.Config{}
			cfg.JWT.Secret = "http-to-ws-remote-v2-nonstream-secret-" + strconv.Itoa(tc.doneCount)
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

			doneEvent := []byte(`{"type":"response.output_item.done","item":{"id":"cmp_http_to_ws_remote_v2","type":"compaction","encrypted_content":"same"}}`)
			events := make([][]byte, 0, tc.doneCount+1)
			for i := 0; i < tc.doneCount; i++ {
				events = append(events, append([]byte(nil), doneEvent...))
			}
			events = append(events, []byte(`{"type":"response.completed","response":{"id":"resp_http_to_ws_remote_v2","status":"completed","model":"gpt-5.1","output":[{"id":"cmp_terminal_http_to_ws_remote_v2","type":"compaction","encrypted_content":"terminal"}],"usage":{"input_tokens":2,"output_tokens":1}}}`))
			upstreamConn := &openAIWSCaptureConn{events: events}
			captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(captureDialer)
			defer pool.Close()

			windowCache := &passthroughCompactWindowCache{}
			svc := &OpenAIGatewayService{
				cfg: cfg,
				settingService: NewSettingService(&openAIUUIDv7RuntimeRepo{values: map[string]string{
					SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
				}}, nil),
				httpUpstream:     &httpUpstreamRecorder{},
				cache:            windowCache,
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
				toolCorrector:    NewCodexToolCorrector(),
				openaiWSPool:     pool,
			}
			account := &Account{
				ID:          4244,
				Name:        "http-to-ws-remote-v2-nonstream",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"access_token": "oauth-token"},
				Extra: map[string]any{
					"responses_websockets_v2_enabled": true,
					openAIPinnedInstallationIDKey:     transportTestPinnedInstallationID,
				},
			}

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.150.1")
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set(openAIWSTurnMetadataHeader,
				`{"session_id":`+strconv.Quote(threadID.String())+
					`,"thread_id":`+strconv.Quote(threadID.String())+
					`,"turn_id":`+strconv.Quote(turnID.String())+
					`,"turn_started_at_unix_ms":1,"request_kind":"compaction"}`,
			)
			c.Set("api_key", &APIKey{ID: 7427})
			MarkOpenAINativeCompactionV2(c)

			body := []byte(`{
				"model":"gpt-5.1",
				"stream":false,
				"input":[{"type":"compaction_trigger"}]
			}`)
			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.OpenAIWSMode)
			require.False(t, result.Stream)
			require.Equal(t, "resp_http_to_ws_remote_v2", result.RequestID)
			require.Equal(t, "cmp_terminal_http_to_ws_remote_v2", gjson.GetBytes(recorder.Body.Bytes(), "output.0.id").String())

			upstreamConn.mu.Lock()
			require.Len(t, upstreamConn.writes, 1)
			forwarded := requestToJSONString(upstreamConn.writes[0])
			upstreamConn.mu.Unlock()
			metadata := gjson.Get(forwarded, "client_metadata."+openAIWSTurnMetadataHeader).String()
			require.Equal(t, "compaction", gjson.Get(metadata, "request_kind").String())
			require.Equal(t, CodexCompactionImplementationRemoteV2, gjson.Get(metadata, "compaction.implementation").String())
			requestContextWindowID := gjson.Get(metadata, "context_window_id").String()
			_, err = canonicalUUIDv7(requestContextWindowID)
			require.NoError(t, err)

			windowCache.mu.Lock()
			commitCalls := windowCache.commitCalls
			commitResult := windowCache.lastResult
			windowCache.mu.Unlock()
			require.Equal(t, tc.expectCommit, commitCalls)
			if tc.expectCommit == 1 {
				require.Equal(t, uint64(1), commitResult.Snapshot.Number)
				require.NotEqual(t, requestContextWindowID, commitResult.Snapshot.ContextWindowID)
			} else {
				contextPlan, ok := OpenAIOAuthIdentityPlanFromContext(c)
				require.True(t, ok)
				require.Zero(t, contextPlan.Window.Number)
				require.Equal(t, requestContextWindowID, contextPlan.Window.ContextWindowID)
			}
		})
	}
}
