//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMergeCodexImportRealPostgresRemapsCollectorAndDropsRuntime(t *testing.T) {
	ctx := context.Background()
	policyRevision := installCodexStateModelPolicyFixture(t, []string{"gpt-5.4"})
	client := testEntClient(t)
	accounts := newAccountRepositoryWithSQL(client, integrationDB, nil)
	proxies := NewProxyRepository(client, integrationDB)
	// Nil probe/privacy dependencies ensure that synthetic imports cannot send
	// requests to the proxy or an OAuth provider.
	admin := service.NewAdminService(nil, nil, nil, accounts, proxies, nil, nil, nil, nil, nil, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := adminhandler.NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/admin/accounts/data", handler.ExportData)
	router.POST("/admin/accounts/data", handler.ImportData)

	suffix := uuid.NewString()
	accountName := "merge-import-" + suffix
	businessName, collectorName := "merge-business-"+suffix, "merge-collector-"+suffix
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id IN (SELECT id FROM accounts WHERE name=$1)", accountName)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE name=$1", accountName)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE name IN ($1,$2)", businessName, collectorName)
	})
	business := mustCreateProxy(t, client, &service.Proxy{Name: businessName, Protocol: "http", Host: "business-" + suffix + ".invalid", Port: 8080})
	collector := mustCreateProxy(t, client, &service.Proxy{Name: collectorName, Protocol: "socks5", Host: "collector-" + suffix + ".invalid", Port: 1080})
	source := mustCreateAccount(t, client, &service.Account{
		Name: accountName, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "synthetic-import-credential", "plan_type": "team"}, ProxyID: &business.ID,
		Extra: map[string]any{
			"note":                                   "portable configuration",
			service.CodexTurnStateExtraKey:           map[string]any{"enabled": true, "account_type": "team_business", "collector_proxy_id": collector.ID},
			service.CodexTurnStateGenerationExtraKey: "source-generation-must-not-copy",
			"codex_turn_state_token":                 "source-token-must-not-copy",
			"codex_turn_state_runtime":               map[string]any{"token": "source-runtime-must-not-copy"},
		},
	})
	runtime := NewOpenAICodexStateRepository(integrationDB, integrationRedis)
	key := service.CodexTurnStateKey{OwnerAccountID: source.ID, Model: "gpt-5.4", Generation: "source-generation-must-not-copy"}
	now := time.Now().UTC()
	state, err := runtime.BeginBusiness(ctx, key, "synthetic-export", now, now.Add(time.Minute))
	require.NoError(t, err)
	state.EncryptedToken, state.Source, state.Shape = "source-encrypted-token-must-not-copy", "business", "accepted"
	state.IssuedAt, state.ExpiresAt, state.TokenLength, state.CipherBlocks = now, now.Add(time.Hour), 332, 12
	state.ModelPolicyRevision = policyRevision
	saved, err := runtime.SaveCAS(ctx, *state, state.Version)
	require.NoError(t, err)
	require.True(t, saved)

	exportedResponse := httptest.NewRecorder()
	router.ServeHTTP(exportedResponse, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/admin/accounts/data?ids=%d", source.ID), nil))
	require.Equal(t, http.StatusOK, exportedResponse.Code, exportedResponse.Body.String())
	require.NotContains(t, exportedResponse.Body.String(), "must-not-copy")
	var exported struct {
		Data adminhandler.DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(exportedResponse.Body.Bytes(), &exported))
	require.Len(t, exported.Data.Accounts, 1)
	require.Len(t, exported.Data.Proxies, 2)
	require.NotNil(t, exported.Data.Accounts[0].CodexTurnStateProxyKey)
	require.Nil(t, exported.Data.Accounts[0].CodexTurnState.CollectorProxyID)

	// Remove the source records so import must create a different local ID for
	// each portable key, rather than accidentally passing by reusing an ID.
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", source.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM proxies WHERE id IN ($1,$2)", business.ID, collector.ID)
	require.NoError(t, err)
	body, err := json.Marshal(adminhandler.DataImportRequest{Data: exported.Data})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/admin/accounts/data", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	importedResponse := httptest.NewRecorder()
	router.ServeHTTP(importedResponse, request)
	require.Equal(t, http.StatusOK, importedResponse.Code, importedResponse.Body.String())
	var imported struct {
		Data adminhandler.DataImportResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(importedResponse.Body.Bytes(), &imported))
	require.Empty(t, imported.Data.Errors)
	require.Equal(t, 2, imported.Data.ProxyCreated)
	require.Equal(t, 1, imported.Data.AccountCreated)

	var accountID, businessID, collectorID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM accounts WHERE name=$1", accountName).Scan(&accountID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM proxies WHERE name=$1", businessName).Scan(&businessID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM proxies WHERE name=$1", collectorName).Scan(&collectorID))
	require.NotEqual(t, source.ID, accountID)
	require.NotEqual(t, business.ID, businessID)
	require.NotEqual(t, collector.ID, collectorID)
	stored, err := accounts.GetByID(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, &businessID, stored.ProxyID)
	config := service.CodexTurnStateConfigForAccount(stored)
	require.True(t, config.Enabled)
	require.Equal(t, "team_business", config.AccountType)
	require.Equal(t, &collectorID, config.CollectorProxyID)
	require.NotEmpty(t, service.CodexTurnStateGenerationForAccount(stored))
	require.NotEqual(t, key.Generation, service.CodexTurnStateGenerationForAccount(stored))
	require.NotContains(t, stored.Extra, "codex_turn_state_token")
	require.NotContains(t, stored.Extra, "codex_turn_state_runtime")
	states, err := runtime.ListByAccount(ctx, accountID)
	require.NoError(t, err)
	require.Empty(t, states)
	count, err := proxies.CountAccountsByProxyID(ctx, collectorID)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.ErrorIs(t, proxies.Delete(ctx, collectorID), service.ErrProxyInUse)
}
