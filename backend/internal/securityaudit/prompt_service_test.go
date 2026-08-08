package securityaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type staticSettingRepository struct {
	values map[string]string
}

func (r staticSettingRepository) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r staticSettingRepository) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}
func (r staticSettingRepository) Set(context.Context, string, string) error { return nil }
func (r staticSettingRepository) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		result[key] = r.values[key]
	}
	return result, nil
}
func (r staticSettingRepository) SetMultiple(context.Context, map[string]string) error { return nil }
func (r staticSettingRepository) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}
func (r staticSettingRepository) Delete(context.Context, string) error { return nil }

func TestPromptServiceHasExplicitIdempotentLifecycle(t *testing.T) {
	config := NewConfigManager(nil, staticSettingRepository{values: map[string]string{
		SettingKeyPromptAuditConfig: "",
		SettingKeyRiskControl:       "false",
	}}, nil, prefixEncryptor{}, testTotpKeyConfig())
	service := NewPromptService(
		config,
		NewPostgreSQLRepository(nil),
		NewRedisPayloadStore(nil),
		NewOpenAICompatibleScanner(),
		NewAtomicMetrics(),
	)

	require.Nil(t, service.cancel, "construction must not start background work")
	require.NoError(t, service.Start(context.Background()))
	require.NotNil(t, service.cancel)
	require.NoError(t, service.Start(context.Background()), "Start must be idempotent")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, service.Shutdown(ctx))
	require.Nil(t, service.cancel)
	require.NoError(t, service.Shutdown(ctx), "Shutdown must be idempotent")
}

func TestPromptServiceStartReportsDependencyFailureWithoutPanic(t *testing.T) {
	service := &PromptService{}
	require.Error(t, service.Start(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, service.Shutdown(ctx))
}

func TestPromptServiceBlockingLatestTurnOnlyUsesNarrowSnapshot(t *testing.T) {
	seen := make([]string, 0, 2)
	evaluator := newGuardEvaluator(PromptScannerFunc(func(_ context.Context, _ ActiveEndpoint, chunk string, _ []string) (*NormalizedResult, error) {
		seen = append(seen, chunk)
		return &NormalizedResult{Decision: EventPass, RiskLevel: RiskLow, Action: ActionAllow, ScannerScores: map[string]float64{}, ScannerEvidence: map[string]string{}}, nil
	}), nil, NewAtomicMetrics(), 2, 2)
	service := &PromptService{
		config: &fakeConfigStore{active: true, cfg: ActiveConfig{
			RiskControlEnabled: true, Enabled: true, BlockingEnabled: true, BlockingLatestTurnOnly: true, AllGroups: true,
			Scanners: AllScannerIDs, Endpoints: []ActiveEndpoint{{ID: "guard-1", Enabled: true, TimeoutMS: 1000, InputLimit: 4096}},
		}},
		evaluator: evaluator,
	}
	decision, err := service.Evaluate(context.Background(), Request{Protocol: "openai_chat_completions", Body: []byte(`{"messages":[{"role":"system","content":"system instruction"},{"role":"user","content":"older user input"},{"role":"assistant","content":"previous output"},{"role":"user","content":"latest user input"}]}`)})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)
	require.Equal(t, []string{"latest user input", "previous output"}, seen)
}

func TestPromptServiceKeywordOnlyBlocksBeforeGuardAndScansAllPromptRoles(t *testing.T) {
	active, err := ActiveFromStorage(storageConfig{
		KeywordBlockingEnabled: true,
		BlockedKeywords:        []string{"forbidden"},
		AllGroups:              true,
		ConfigVersion:          12,
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	metrics := NewAtomicMetrics()
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: active}, metrics: metrics, clock: fixedClock{now: time.Unix(100, 0).UTC()}}

	decision, err := service.CheckKeyword(context.Background(), Request{
		RequestID: "keyword-request", Protocol: "openai_chat_completions",
		Body: []byte(`{"messages":[{"role":"system","content":"safe"},{"role":"developer","content":"safe"},{"role":"assistant","content":"safe"},{"role":"tool","content":"FORBIDDEN"}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	require.Equal(t, DecisionBlock, decision.Kind)
	require.Equal(t, ErrorCodeKeywordBlocked, decision.ErrorCode)
	require.Equal(t, "keyword", decision.Result.Categories[0])
	require.Equal(t, "local-keyword", decision.Result.ScannerBackend)
	digest := sha256.Sum256([]byte("forbidden"))
	require.Equal(t, "sha256:"+hex.EncodeToString(digest[:]), decision.Result.ScannerEvidence["keyword"], "evidence must be a hash, not plaintext")
	require.NotContains(t, decision.Result.ScannerEvidence["keyword"], "forbidden")
	require.Equal(t, int64(1), metrics.Snapshot().Blocked)
	require.Equal(t, int64(1), metrics.Snapshot().KeywordBlocked)
}

func TestPromptServiceKeywordPolicySkipsOutOfScopeAndNoTextAndFailsClosedOnInvalidJSON(t *testing.T) {
	active, err := ActiveFromStorage(storageConfig{
		KeywordBlockingEnabled: true,
		BlockedKeywords:        []string{"forbidden"},
		AllGroups:              false,
		GroupIDs:               []int64{9},
		ConfigVersion:          12,
	}, true, prefixEncryptor{})
	require.NoError(t, err)
	service := &PromptService{config: &fakeConfigStore{active: true, cfg: active}, metrics: NewAtomicMetrics(), clock: fixedClock{now: time.Unix(100, 0).UTC()}}
	group := int64(8)
	decision, err := service.CheckKeyword(context.Background(), Request{GroupID: &group, Protocol: "openai_chat_completions", Body: []byte(`{"messages":[{"role":"user","content":"forbidden"}]}`)})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)

	decision, err = service.CheckKeyword(context.Background(), Request{Protocol: "openai_chat_completions", Body: []byte(`{"messages":[{"role":"function","content":"forbidden"}]}`)})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision.Kind)

	group = 9
	decision, err = service.CheckKeyword(context.Background(), Request{GroupID: &group, Protocol: "openai_chat_completions", Body: []byte(`{"messages":[`)})
	require.Error(t, err)
	require.Nil(t, decision)
	var guardErr *GuardError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeInvalidResponse, guardErr.Code)
}

func TestPromptServiceRejectsInvalidDeleteConfirmationClaims(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	filter := EventFilter{Decision: string(EventCritical), StartAt: &start, EndAt: &end}
	const snapshotMaxID int64 = 10
	filterHash := FilterHash(filter, snapshotMaxID)
	validClaims := deleteClaims{
		FilterHash: filterHash, SnapshotMaxID: snapshotMaxID, AdminID: 7,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	claimsToken := func(claims deleteClaims) string {
		raw, err := json.Marshal(claims)
		require.NoError(t, err)
		return string(raw)
	}
	validRequest := DeleteByFilterRequest{
		Filter: filter, SnapshotMaxID: snapshotMaxID, FilterHash: filterHash,
		ConfirmationToken: claimsToken(validClaims), Confirm: true,
	}

	tests := []struct {
		name    string
		request DeleteByFilterRequest
		adminID int64
	}{
		{name: "confirm false", request: func() DeleteByFilterRequest { value := validRequest; value.Confirm = false; return value }(), adminID: 7},
		{name: "malformed token", request: func() DeleteByFilterRequest {
			value := validRequest
			value.ConfirmationToken = "not-json"
			return value
		}(), adminID: 7},
		{name: "different administrator", request: validRequest, adminID: 8},
		{name: "filter hash mismatch", request: func() DeleteByFilterRequest {
			value := validRequest
			value.FilterHash = strings.Repeat("b", 64)
			return value
		}(), adminID: 7},
		{name: "snapshot mismatch", request: func() DeleteByFilterRequest { value := validRequest; value.SnapshotMaxID++; return value }(), adminID: 7},
		{name: "expired", request: func() DeleteByFilterRequest {
			value := validRequest
			claims := validClaims
			claims.ExpiresAt = now
			value.ConfirmationToken = claimsToken(claims)
			return value
		}(), adminID: 7},
	}

	service := &PromptService{config: &fakeConfigStore{}, clock: fixedClock{now: now}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := service.DeleteByFilter(context.Background(), test.request, test.adminID)
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
}
