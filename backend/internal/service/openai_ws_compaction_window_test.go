package service

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newOpenAICodexWSCompactWindowTestPlan(t *testing.T, secret string, apiKeyID int64) (*OpenAIGatewayService, *Account, OpenAIOAuthIdentityPlan) {
	t.Helper()
	threadID, err := uuid.NewV7()
	require.NoError(t, err)
	turnID, err := uuid.NewV7()
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
			TurnID:      CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: turnID.String()},
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
	plan, err = BindOpenAICodexWindowToPlan(plan, OpenAICodexWindowSnapshot{ThreadID: threadID.String()}, mappingKey)
	require.NoError(t, err)
	return &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: secret}}},
		&Account{ID: 4242, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, plan
}

func TestObserveOpenAICodexWSCompactionDeliveryRequiresDeliveredSuccessfulTerminal(t *testing.T) {
	compactItem := []byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction"}}`)

	tests := []struct {
		name     string
		terminal []byte
		valid    bool
	}{
		{name: "completed", terminal: []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"id":"cmp_1","type":"compaction"}]}}`), valid: true},
		{name: "failed event", terminal: []byte(`{"type":"response.failed","response":{"status":"failed"}}`), valid: false},
		{name: "completed envelope with incomplete status", terminal: []byte(`{"type":"response.completed","response":{"status":"incomplete","output":[{"id":"cmp_1","type":"compaction"}]}}`), valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delivery := &openAICodexCompactionDelivery{}
			observeOpenAICodexWSCompactionDelivery(delivery, compactItem)
			observeOpenAICodexWSCompactionDelivery(delivery, tt.terminal)
			require.Equal(t, tt.valid, delivery.Valid())
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
	delivery := openAICodexWSCompactionDeliveryForPlan(account, plan)
	require.NotNil(t, delivery)
	observeOpenAICodexWSCompactionDelivery(delivery, []byte(`{"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction"}}`))

	require.False(t, svc.commitOpenAICodexWSCompactionAfterDelivery(context.Background(), nil, account, &plan, delivery, ""))
	require.Zero(t, plan.Window.Number)
}
