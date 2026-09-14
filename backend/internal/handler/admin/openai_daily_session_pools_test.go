package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type dailyPoolSettingStub struct {
	service.SettingRepository
	enabled bool
}

func (s *dailyPoolSettingStub) GetValue(context.Context, string) (string, error) {
	return strconv.FormatBool(s.enabled), nil
}

type dailyPoolReaderStub struct {
	// Unimplemented writer methods panic if the read-only handler calls them.
	service.OAuthDailySessionRepository
	pool  service.OAuthDailySessionPool
	calls int
	ids   []int64
}

func (s *dailyPoolReaderStub) ListOAuthDailySessionPools(_ context.Context, ids []int64, _ time.Time) (map[int64]service.OAuthDailySessionPool, error) {
	s.calls++
	s.ids = ids
	if s.pool.AccountID == 0 {
		return map[int64]service.OAuthDailySessionPool{}, nil
	}
	return map[int64]service.OAuthDailySessionPool{s.pool.AccountID: s.pool}, nil
}

func TestListOAuthDailySessionPoolsJSONMatchesAccountPage(t *testing.T) {
	reader := &dailyPoolReaderStub{pool: service.OAuthDailySessionPool{
		AccountID: 42, BusinessDate: "2026-09-14",
		Generation: "01994000-0000-7000-8000-000000000001",
		StreamSessionIDs: [3]string{
			"01994000-0000-7000-8000-000000000010",
			"01994000-0000-7000-8000-000000000011",
			"01994000-0000-7000-8000-000000000012",
		},
		SyncSessionID: "01994000-0000-7000-8000-000000000020",
	}}
	h := &OpenAIOAuthHandler{
		dailySessionPools: reader,
		settingService:    service.NewSettingService(&dailyPoolSettingStub{enabled: true}, nil),
	}
	recorder := invokeFingerprintObservationHandler(t, "/api/v1/admin/openai/daily-session-pools?account_ids=42,43", h.ListOAuthDailySessionPools)
	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	// The browser tests use this same fixture through the real HTTP client.
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "frontend", "src", "__tests__", "fixtures", "oauthDailySessionPools.json"))
	require.NoError(t, err)
	var expected map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fixture, &expected))
	require.JSONEq(t, string(expected["items"]), string(envelope.Data["items"]))
	require.JSONEq(t, "true", string(envelope.Data["enabled"]))
	require.Equal(t, []int64{42, 43}, reader.ids)
	require.Equal(t, 1, reader.calls)
}

func TestListOAuthDailySessionPoolsEmptyStatesStayReadOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			reader := &dailyPoolReaderStub{}
			h := &OpenAIOAuthHandler{
				dailySessionPools: reader,
				settingService:    service.NewSettingService(&dailyPoolSettingStub{enabled: enabled}, nil),
			}
			recorder := invokeFingerprintObservationHandler(t, "/api/v1/admin/openai/daily-session-pools?account_ids=42", h.ListOAuthDailySessionPools)
			require.Equal(t, http.StatusOK, recorder.Code)
			var envelope struct {
				Data struct {
					Enabled bool                       `json:"enabled"`
					Items   map[string]json.RawMessage `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.Equal(t, enabled, envelope.Data.Enabled)
			require.Empty(t, envelope.Data.Items)
			if enabled {
				require.Equal(t, 1, reader.calls)
			} else {
				require.Zero(t, reader.calls)
			}
		})
	}
}
