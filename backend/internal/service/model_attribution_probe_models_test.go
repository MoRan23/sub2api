package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAttributionProbeModelsUseTargetsAndRawCatalog(t *testing.T) {
	future := time.Now().Add(time.Hour)
	account := Account{ID: 1, Name: "probe", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusDisabled,
		RateLimitResetAt: &future, Credentials: map[string]any{"model_mapping": map[string]any{
			"public-alias": "actual-target", "low": "low", "wild-*": "fixed-target", "invalid": "target-*",
		}}}
	repo := &stubOpenAIAccountRepo{accounts: []Account{account}}
	s := NewModelAttributionService(nil, repo, nil)
	s.fetchProbeModels = func(_ context.Context, a *Account) (*OpenAIModelsResponse, error) {
		require.EqualValues(t, 1, a.ID)
		return &OpenAIModelsResponse{Body: []byte(`{"data":[{"id":"upstream-high"},{"id":"actual-target"}]}`)}, nil
	}
	out, err := s.ProbeModels(context.Background(), []int64{1, 1})
	require.NoError(t, err)
	require.Len(t, out.Accounts, 1)
	require.Equal(t, []string{"actual-target", "fixed-target", "low", "upstream-high"}, out.Models)
	require.Equal(t, out.Models, out.Accounts[0].Models)
	require.Equal(t, "success", out.Accounts[0].Status)
	require.Empty(t, out.Accounts[0].Error)
	mapping, ok := account.Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "actual-target", mapping["public-alias"])
}

func TestAttributionProbeModelsFailuresAndCoverage(t *testing.T) {
	accounts := []Account{
		{ID: 1, Name: "configured", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "actual"}}},
		{ID: 2, Name: "no config", Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 3, Name: "other", Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		{ID: 4, Name: "empty", Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{ID: 5, Name: "live", Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}
	s := NewModelAttributionService(nil, &stubOpenAIAccountRepo{accounts: accounts}, nil)
	s.fetchProbeModels = func(_ context.Context, account *Account) (*OpenAIModelsResponse, error) {
		switch account.ID {
		case 4:
			return &OpenAIModelsResponse{Body: []byte(`{"data":[]}`)}, nil
		case 5:
			return &OpenAIModelsResponse{Body: []byte(`{"data":[{"id":"live"},{"id":"actual"}]}`)}, nil
		default:
			return nil, errors.New("Bearer private-key and private upstream response")
		}
	}
	out, err := s.ProbeModels(context.Background(), []int64{1, 2, 3, 4, 5, 99})
	require.NoError(t, err)
	require.Equal(t, []string{"actual", "live"}, out.Models)
	require.Equal(t, "partial", out.Accounts[0].Status)
	require.Equal(t, []string{"actual"}, out.Accounts[0].Models)
	require.Equal(t, "model_catalog_failed", out.Accounts[0].Error)
	require.Equal(t, "error", out.Accounts[1].Status)
	require.Equal(t, "unsupported_account", out.Accounts[2].Error)
	require.Equal(t, "success", out.Accounts[3].Status)
	require.Empty(t, out.Accounts[3].Models)
	require.Equal(t, "success", out.Accounts[4].Status)
	require.Equal(t, "account_missing", out.Accounts[5].Error)
	body, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(body), "private")
}

func TestAttributionProbeModelsValidationAndBoundedConcurrency(t *testing.T) {
	s := NewModelAttributionService(nil, &stubOpenAIAccountRepo{}, nil)
	for _, ids := range [][]int64{nil, {0}, {-1}, make([]int64, 501)} {
		_, err := s.ProbeModels(context.Background(), ids)
		require.ErrorIs(t, err, ErrAttributionInvalid)
	}
	accounts, ids := []Account{}, []int64{}
	for id := int64(1); id <= 9; id++ {
		accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
		ids = append(ids, id)
	}
	s.accounts = &stubOpenAIAccountRepo{accounts: accounts}
	var active, peak atomic.Int32
	s.fetchProbeModels = func(ctx context.Context, account *Account) (*OpenAIModelsResponse, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
			return &OpenAIModelsResponse{Body: []byte(fmt.Sprintf(`{"data":[{"id":"model-%d"}]}`, account.ID))}, nil
		}
	}
	out, err := s.ProbeModels(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, out.Models, 9)
	require.LessOrEqual(t, peak.Load(), int32(3))
	require.Greater(t, peak.Load(), int32(1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.ProbeModels(ctx, ids)
	require.ErrorIs(t, err, context.Canceled)
}
