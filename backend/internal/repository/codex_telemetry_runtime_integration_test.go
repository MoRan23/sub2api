//go:build integration

package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type telemetryRuntimeAccounts struct {
	service.AccountRepository
	account *service.Account
}

func (r telemetryRuntimeAccounts) GetByID(context.Context, int64) (*service.Account, error) {
	return r.account, nil
}

// Exercise the production producer -> PostgreSQL -> restored profile -> sender
// boundary. The sender is entirely local: no business or telemetry network calls.
func TestCodexTelemetryPostgresRuntimeThreeSystemsAcrossRestart(t *testing.T) {
	store, key, _ := telemetryStoreFixture(t)
	t.Setenv("CODEX_TELEMETRY_ENABLED", "true")
	owner := key.OwnerAccountID
	profiles := map[string]service.OpenAIOAuthOSProfile{}
	inputs := map[string]service.CodexTelemetryInput{}
	for os, ua := range map[string]string{
		"windows": "codex_cli_rs/0.155.1 (Windows 10.0.26200; x86_64)",
		"macos":   "codex_cli_rs/0.155.1 (Mac OS 15.6.1; aarch64)",
		"linux":   "codex_cli_rs/0.155.1 (Ubuntu 24.04; x86_64)",
	} {
		installation := uuid.NewString()
		profiles[os] = service.OpenAIOAuthOSProfile{OSFamily: os, InstallationID: installation, UserAgent: ua}
		inputs[os] = service.CodexTelemetryInput{AccountID: owner, OwnerAccountID: owner, OSFamily: os,
			InstallationID: installation, ManagedInstallation: true, AccessToken: "stale-request-token",
			ChatGPTAccountID: "integration-workspace", UserAgent: ua, Originator: "codex_cli_rs", Version: "0.155.1",
			SessionID: uuid.NewString(), ThreadID: uuid.NewString(), TurnID: uuid.NewString(), SamplingID: uuid.NewString(),
			Model: "gpt-6-astra", StartedAt: time.Now().Add(-time.Second)}
	}
	type sentRequest struct{ authorization, workspace, os string }
	var mu sync.Mutex
	var sent []sentRequest
	newRuntime := func(token string) *service.CodexTelemetryService {
		runtime := service.NewCodexTelemetryService(nil)
		runtime.SetSender(func(_ context.Context, req *http.Request, in service.CodexTelemetryInput, _ bool) (*http.Response, error) {
			mu.Lock()
			sent = append(sent, sentRequest{req.Header.Get("Authorization"), req.Header.Get("ChatGPT-Account-ID"), in.OSFamily})
			mu.Unlock()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})
		account := &service.Account{ID: owner, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive,
			Credentials:           map[string]any{"access_token": token, "chatgpt_account_id": "integration-workspace"},
			OpenAIOAuthOSProfiles: &service.OpenAIOAuthOSProfiles{DefaultOS: "windows", Profiles: profiles}}
		runtime.SetPersistence(store, telemetryRuntimeAccounts{account: account}, nil)
		t.Cleanup(runtime.Stop)
		return runtime
	}
	first := newRuntime("latest-first-token")
	for _, input := range inputs {
		attempt := first.Begin(context.Background(), input)
		require.NotNil(t, attempt)
		attempt.Finish(service.CodexTelemetryResult{Status: "completed", HTTPStatus: 200, DeliveryStatus: "delivered",
			ResponseID: uuid.NewString(), InputTokens: 7, OutputTokens: 3, FinishedAt: time.Now()})
	}
	count := func(query string) int {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(context.Background(), query, owner).Scan(&n))
		return n
	}
	require.Eventually(t, func() bool {
		return count(`SELECT count(*) FROM codex_telemetry_activities a JOIN codex_telemetry_pools p ON p.id=a.pool_id
			WHERE p.owner_account_id=$1 AND a.kind='attempt' AND (a.state->>'finished')::boolean`) == 3 &&
			count(`SELECT count(*) FROM codex_telemetry_batches b JOIN codex_telemetry_pools p ON p.id=b.pool_id
			WHERE p.owner_account_id=$1 AND b.status='sent'`) == 3
	}, 8*time.Second, 20*time.Millisecond)
	first.Stop()
	require.Equal(t, 3, count(`SELECT count(*) FROM codex_telemetry_pools WHERE owner_account_id=$1`))
	var firstSeeds string
	require.NoError(t, integrationDB.QueryRow(`SELECT string_agg(seed::text,',' ORDER BY os_family) FROM codex_telemetry_pools WHERE owner_account_id=$1`, owner).Scan(&firstSeeds))

	second := newRuntime("latest-refreshed-token")
	for _, input := range inputs {
		input.TurnID, input.SamplingID, input.StartedAt = uuid.NewString(), uuid.NewString(), time.Now()
		attempt := second.Begin(context.Background(), input)
		require.NotNil(t, attempt)
		attempt.Finish(service.CodexTelemetryResult{Status: "completed", HTTPStatus: 200, DeliveryStatus: "delivered", FinishedAt: time.Now()})
	}
	require.Eventually(t, func() bool {
		return count(`SELECT count(*) FROM codex_telemetry_activities a JOIN codex_telemetry_pools p ON p.id=a.pool_id
			WHERE p.owner_account_id=$1 AND a.kind='turn' AND (a.state->>'sealed')::boolean`) == 3 &&
			count(`SELECT count(*) FROM codex_telemetry_batches b JOIN codex_telemetry_pools p ON p.id=b.pool_id
			WHERE p.owner_account_id=$1 AND b.status='sent'`) >= 6
	}, 8*time.Second, 20*time.Millisecond)
	second.Stop()
	var secondSeeds string
	require.NoError(t, integrationDB.QueryRow(`SELECT string_agg(seed::text,',' ORDER BY os_family) FROM codex_telemetry_pools WHERE owner_account_id=$1`, owner).Scan(&secondSeeds))
	require.Equal(t, firstSeeds, secondSeeds)
	for _, input := range inputs {
		var initializations int
		require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM codex_telemetry_batches b JOIN codex_telemetry_pools p ON p.id=b.pool_id,
			LATERAL jsonb_array_elements(COALESCE(b.payload->'events','[]'::jsonb)) e
			WHERE p.owner_account_id=$1 AND e->>'event_type'='codex_thread_initialized' AND e->'event_params'->>'thread_id'=$2`, owner, input.ThreadID).Scan(&initializations))
		require.Equal(t, 1, initializations, input.OSFamily)
	}
	var containsSecrets bool
	require.NoError(t, integrationDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM codex_telemetry_batches b JOIN codex_telemetry_pools p ON p.id=b.pool_id
		WHERE p.owner_account_id=$1 AND (b.payload::text || b.metadata::text) ~ '(stale-request-token|latest-first-token|latest-refreshed-token)')`, owner).Scan(&containsSecrets))
	require.False(t, containsSecrets)
	mu.Lock()
	defer mu.Unlock()
	seenRefreshed := map[string]bool{}
	for _, request := range sent {
		require.Equal(t, "integration-workspace", request.workspace)
		require.NotEqual(t, "Bearer stale-request-token", request.authorization)
		if request.authorization == "Bearer latest-refreshed-token" {
			seenRefreshed[request.os] = true
		}
	}
	require.Len(t, seenRefreshed, 3)
}
