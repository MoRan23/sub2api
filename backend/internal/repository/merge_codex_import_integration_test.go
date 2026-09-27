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

	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexImportRealPostgresRetiresCollectorConfiguration(t *testing.T) {
	ctx := context.Background()
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
	businessName, collectorName, alternateName := "merge-business-"+suffix, "merge-collector-"+suffix, "merge-alternate-"+suffix
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id IN (SELECT id FROM accounts WHERE name=$1)", accountName)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE name=$1", accountName)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM proxies WHERE name IN ($1,$2,$3)", businessName, collectorName, alternateName)
	})
	business := mustCreateProxy(t, client, &service.Proxy{Name: businessName, Protocol: "http", Host: "business-" + suffix + ".invalid", Port: 8080})
	collector := mustCreateProxy(t, client, &service.Proxy{Name: collectorName, Protocol: "socks5", Host: "collector-" + suffix + ".invalid", Port: 1080})
	alternate := mustCreateProxy(t, client, &service.Proxy{Name: alternateName, Protocol: "http", Host: "alternate-" + suffix + ".invalid", Port: 8080})
	source := mustCreateAccount(t, client, &service.Account{
		Name: accountName, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "synthetic-import-credential", "plan_type": "team"}, ProxyID: &business.ID,
		Extra: map[string]any{
			"note":                        "portable configuration",
			"codex_turn_state":            map[string]any{"enabled": true, "account_type": "team_business", "collector_proxy_ids": []int64{alternate.ID, collector.ID, business.ID}},
			"codex_turn_state_generation": "source-generation-must-not-copy",
			"codex_turn_state_token":      "source-token-must-not-copy",
			"codex_turn_state_runtime":    map[string]any{"token": "source-runtime-must-not-copy"},
		},
	})
	_, err := accounts.EnsureOpenAIOAuthOSProfiles(ctx, source.ID)
	require.NoError(t, err)
	exportedResponse := httptest.NewRecorder()
	router.ServeHTTP(exportedResponse, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/admin/accounts/data?ids=%d", source.ID), nil))
	require.Equal(t, http.StatusOK, exportedResponse.Code, exportedResponse.Body.String())
	require.NotContains(t, exportedResponse.Body.String(), "must-not-copy")
	var exported struct {
		Data adminhandler.DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(exportedResponse.Body.Bytes(), &exported))
	require.Len(t, exported.Data.Accounts, 1)
	require.Len(t, exported.Data.Proxies, 1)
	require.NotContains(t, exportedResponse.Body.String(), "codex_turn_state")

	// Remove the source records so import must create a different local ID for
	// each portable key, rather than accidentally passing by reusing an ID.
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", source.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM proxies WHERE id IN ($1,$2,$3)", business.ID, collector.ID, alternate.ID)
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
	require.Equal(t, 1, imported.Data.ProxyCreated)
	require.Equal(t, 1, imported.Data.AccountCreated)

	var accountID, businessID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM accounts WHERE name=$1", accountName).Scan(&accountID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM proxies WHERE name=$1", businessName).Scan(&businessID))
	require.NotEqual(t, source.ID, accountID)
	require.NotEqual(t, business.ID, businessID)
	stored, err := accounts.GetByID(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, &businessID, stored.ProxyID)
	require.NotContains(t, stored.Extra, "codex_turn_state")
	require.NotContains(t, stored.Extra, "codex_turn_state_token")
	require.NotContains(t, stored.Extra, "codex_turn_state_runtime")
	for _, id := range []int64{businessID} {
		count, err := proxies.CountAccountsByProxyID(ctx, id)
		require.NoError(t, err)
		require.EqualValues(t, 1, count, "only the ordinary account proxy is retained")
		require.ErrorIs(t, proxies.Delete(ctx, id), service.ErrProxyInUse)
	}
}
