package service

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func codexStateIdentityFixture(t *testing.T) (*CodexTurnStateService, *Account) {
	t.Helper()
	isolateCodexTurnStateSummaryStore(t)
	owner := codexStateTestScopeAccount(codexStateBatchOwner(7, false))
	owner.Extra[CodexTurnStateCredentialEpochExtraKey] = "private-epoch-first"
	accounts := &codexStateBatchAccounts{accounts: map[int64]*Account{7: owner, 17: codexStateBatchShadow(17, 7)}}
	state := NewCodexTurnStateService(&codexStateBatchRecords{}, accounts, nil, nil)
	state.modelPolicy = &codexStateBatchPolicy{models: []string{"gpt-6-astra"}}
	return state, owner
}

func prepareCodexIdentityObservation(t *testing.T, state *CodexTurnStateService, owner *Account, physicalToken string) *CodexTurnStateAttempt {
	t.Helper()
	attempt, err := state.Prepare(context.Background(), owner, "gpt-6-astra")
	require.NoError(t, err)
	require.NotNil(t, attempt)
	headers := http.Header{"Authorization": {"Bearer " + physicalToken}}
	state.bindHistoryCredentials(context.Background(), attempt, headers)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	noteOpenAICodexStatePatch(c, attempt, nil, nil)
	wire := populateCodexTurnStateObservation(c, nil, headers, nil, false)
	bindCodexTurnStateSummarySequence(wire)
	return attempt
}

func finishCodexIdentityObservation(t *testing.T, state *CodexTurnStateService, attempt *CodexTurnStateAttempt, token string) {
	t.Helper()
	state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {token}})
	require.NoError(t, state.Finish(context.Background(), attempt, true))
	finishOpenAICodexStateObservation(attempt)
}

func TestCodexTurnStateObservationIdentityCredentialReplacementIsolatesLateResponses(t *testing.T) {
	state, owner := codexStateIdentityFixture(t)
	current := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
	late := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
	finishCodexIdentityObservation(t, state, current, codexStateTestToken(10, time.Now()))
	before, err := state.GetStatus(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, before.Observations, 1)
	owner.Credentials["access_token"] = "replacement-credential"
	owner.Extra[CodexTurnStateCredentialEpochExtraKey] = "private-epoch-second"
	after, err := state.GetStatus(context.Background(), 7)
	require.NoError(t, err)
	require.Empty(t, after.Observations, "new credentials must not inherit the old credential's colored summary")
	replacement := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
	finishCodexIdentityObservation(t, state, replacement, codexStateTestToken(10, time.Now()))
	newState, err := state.GetStatus(context.Background(), 7)
	require.NoError(t, err)
	finishCodexIdentityObservation(t, state, late, codexStateTestToken(11, time.Now()))
	batch, err := state.GetStatuses(context.Background(), []int64{7, 17})
	require.NoError(t, err)
	require.Equal(t, newState.Observations, batch.Items["7"].Observations, "late old credential response cannot replace a new epoch's summary")
	require.Equal(t, newState.Observations, batch.Items["17"].Observations)
	encoded, err := json.Marshal(batch)
	require.NoError(t, err)
	for _, forbidden := range []string{"private-epoch-first", "private-epoch-second", "replacement-credential", "credential_epoch"} {
		require.NotContains(t, string(encoded), forbidden)
	}
}

func TestCodexTurnStateObservationIdentityUnboundCredentialsDoNotOverrideKnownEpoch(t *testing.T) {
	state, owner := codexStateIdentityFixture(t)
	known := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
	finishCodexIdentityObservation(t, state, known, codexStateTestToken(10, time.Now()))
	before, err := state.GetStatus(context.Background(), 7)
	require.NoError(t, err)
	unbound := prepareCodexIdentityObservation(t, state, owner, "different-physical-credential")
	require.Empty(t, unbound.credentialEpoch)
	finishCodexIdentityObservation(t, state, unbound, codexStateTestToken(11, time.Now()))
	after, err := state.GetStatus(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, before.Observations, after.Observations)
	delete(owner.Extra, CodexTurnStateCredentialEpochExtraKey)
	unknown, err := state.GetStatus(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, unknown.Observations, 1, "legacy accounts without a known epoch can still show unbound passive observations")
	require.Equal(t, 312, unknown.Observations[0].ResponseLength)
}

func TestCodexTurnStateObservationIdentityReprojectsCurrentAccountType(t *testing.T) {
	for _, blocks := range []int{12, 13} {
		t.Run(testBoolName(blocks == 12), func(t *testing.T) {
			state, owner := codexStateIdentityFixture(t)
			delete(owner.Credentials, "plan_type")
			attempt := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
			finishCodexIdentityObservation(t, state, attempt, codexStateTestToken(blocks, time.Now()))
			unknown, err := state.GetStatus(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, "unknown", unknown.Observations[0].ResponseShape)
			owner.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": false, "account_type": "team_business"}
			batch, err := state.GetStatuses(context.Background(), []int64{7, 17})
			require.NoError(t, err)
			want := "target"
			if blocks == 13 {
				want = "suspect"
			}
			require.Equal(t, want, batch.Items["7"].Observations[0].ResponseShape)
			require.Empty(t, batch.Items["7"].Observations[0].ResponseValidationReason)
			require.Equal(t, unknown.Observations[0].ObservedAt, batch.Items["7"].Observations[0].ObservedAt)
			require.Equal(t, batch.Items["7"].Observations, batch.Items["17"].Observations)
			owner.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": false, "account_type": "personal"}
			personal, err := state.GetStatus(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, "unknown", personal.Observations[0].ResponseShape)
			require.Equal(t, "unexpected_shape", personal.Observations[0].ResponseValidationReason)
			owner.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": false, "account_type": "auto"}
			owner.Credentials["plan_type"] = "self_serve_business_prolite"
			auto, err := state.GetStatus(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, want, auto.Observations[0].ResponseShape)
		})
	}
}

func TestCodexTurnStateObservationIdentityNeverPromotesInvalidEnvelopes(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{name: "invalid", token: strings.Repeat("x", 332)},
		{name: "future", token: codexStateTestToken(12, time.Now().Add(time.Hour))},
		{name: "expired", token: codexStateTestToken(12, time.Now().Add(-2*time.Hour))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, owner := codexStateIdentityFixture(t)
			delete(owner.Credentials, "plan_type")
			attempt := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
			finishCodexIdentityObservation(t, state, attempt, tc.token)
			owner.Extra[CodexTurnStateExtraKey] = map[string]any{"account_type": "team_business"}
			status, err := state.GetStatus(context.Background(), 7)
			require.NoError(t, err)
			require.NotContains(t, []string{"target", "suspect"}, status.Observations[0].ResponseShape)
			require.NotEmpty(t, status.Observations[0].ResponseValidationReason)
		})
	}
}

func TestCodexTurnStateObservationIdentityValidationFallbackRetainsOnlyVerifiedIdentity(t *testing.T) {
	for _, matching := range []bool{false, true} {
		t.Run(testBoolName(matching), func(t *testing.T) {
			state, owner := codexStateIdentityFixture(t)
			original := &CodexTurnStateAttempt{OSFamily: "windows", OwnerAccountID: owner.ID, Model: "gpt-6-astra", AccountEnabled: true,
				Generation: "private-config-generation", key: CodexTurnStateKey{OSFamily: "windows", OwnerAccountID: owner.ID, Model: "gpt-6-astra", Generation: "private-config-generation"},
				credentialEpoch: CodexTurnStateCredentialEpochForAccount(owner), historyService: state, preparedAt: time.Now(),
				id: "private-lease", accountType: "personal", Snapshot: CodexTurnStateSnapshot{Token: "not-to-inherit", Version: 2}}
			attempt := passiveCodexStateAfterValidationFailure(original)
			require.Equal(t, CodexTurnStateSnapshot{}, attempt.Snapshot)
			require.Empty(t, attempt.Generation)
			require.Empty(t, attempt.id)
			require.Equal(t, CodexTurnStateKey{}, attempt.key)
			physicalToken := "wrong-token"
			if matching {
				physicalToken = owner.GetCredential("access_token")
			}
			state.bindHistoryCredentials(context.Background(), attempt, http.Header{"Authorization": {"Bearer " + physicalToken}})
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			noteOpenAICodexStatePatch(c, attempt, nil, nil)
			bindCodexTurnStateSummarySequence(populateCodexTurnStateObservation(c, nil, nil, nil, false))
			finishCodexIdentityObservation(t, state, attempt, codexStateTestToken(10, time.Now()))
			status, err := state.GetStatus(context.Background(), owner.ID)
			require.NoError(t, err)
			if matching {
				require.Len(t, status.Observations, 1)
				require.Equal(t, "snapshot_unavailable", status.Observations[0].MaintenanceReason)
			} else {
				require.Empty(t, status.Observations)
			}
		})
	}
}

func TestCodexTurnStateObservationIdentityCollectorRetainsPhysicalEpoch(t *testing.T) {
	state, owner := codexStateIdentityFixture(t)
	oldOwner := *owner
	oldOwner.Extra = maps.Clone(owner.Extra)
	owner.Extra[CodexTurnStateCredentialEpochExtraKey] = "private-new-collector-epoch"
	observe := func(account *Account, blocks int) {
		token := codexStateTestToken(blocks, time.Now())
		envelope, err := InspectCodexTurnStateEnvelope(token, time.Now())
		require.NoError(t, err)
		shape, err := ParseCodexTurnState(token, "personal", time.Now())
		require.NoError(t, err)
		state.recordCollectorObservation(account, "gpt-6-astra", CodexTurnStateCollectResult{Observation: &CodexTurnStateSafeObservation{
			ObservedAt: time.Now(), Shape: shape.Shape, TokenLength: len(token), CipherBlocks: envelope.CipherBlocks,
			ObservedShape: envelope.ObservedShape, EnvelopeValid: true, IssuedAt: envelope.IssuedAt, ExpiresAt: envelope.ExpiresAt,
		}})
	}
	observe(owner, 10)
	before, err := state.GetStatus(context.Background(), owner.ID)
	require.NoError(t, err)
	observe(&oldOwner, 11)
	after, err := state.GetStatus(context.Background(), owner.ID)
	require.NoError(t, err)
	require.Equal(t, before.Observations, after.Observations, "late collector cannot be attributed to replacement credentials")
	require.Equal(t, "target", after.Observations[0].ResponseShape)
}

func TestCodexTurnStateObservationIdentityPreservesValidationAtObservationTime(t *testing.T) {
	state, owner := codexStateIdentityFixture(t)
	delete(owner.Credentials, "plan_type")
	attempt := prepareCodexIdentityObservation(t, state, owner, owner.GetCredential("access_token"))
	finishCodexIdentityObservation(t, state, attempt, codexStateTestToken(12, time.Now().Add(-time.Minute)))
	owner.Extra[CodexTurnStateExtraKey] = map[string]any{"account_type": "team_business"}
	state.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	status, err := state.GetStatus(context.Background(), owner.ID)
	require.NoError(t, err)
	require.Equal(t, "target", status.Observations[0].ResponseShape, "historical envelope shape is not current cache availability")
	require.Empty(t, status.Models)
}
