package admin

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupRedeemExportRouter() (*gin.Engine, *stubAdminService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()

	h := NewRedeemHandler(adminSvc, nil)
	router.GET("/api/v1/admin/redeem-codes/export", h.Export)
	return router, adminSvc
}

func TestRedeemExportPassesSearchAndSort(t *testing.T) {
	router, adminSvc := setupRedeemExportRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/export?type=balance&status=unused&search=ABC&sort_by=value&sort_order=asc", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, adminSvc.lastListRedeemCodes.calls)
	require.Equal(t, "balance", adminSvc.lastListRedeemCodes.codeType)
	require.Equal(t, "unused", adminSvc.lastListRedeemCodes.status)
	require.Equal(t, "ABC", adminSvc.lastListRedeemCodes.search)
	require.Equal(t, "value", adminSvc.lastListRedeemCodes.sortBy)
	require.Equal(t, "asc", adminSvc.lastListRedeemCodes.sortOrder)
}

func TestRedeemExportSortDefaults(t *testing.T) {
	router, adminSvc := setupRedeemExportRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/export", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, adminSvc.lastListRedeemCodes.calls)
	require.Equal(t, "id", adminSvc.lastListRedeemCodes.sortBy)
	require.Equal(t, "desc", adminSvc.lastListRedeemCodes.sortOrder)
}

func TestRedeemExportPreservesEightDecimalWalletPrecision(t *testing.T) {
	router, adminSvc := setupRedeemExportRouter()
	adminSvc.redeems = []service.RedeemCode{{
		ID:        1,
		Code:      "BALANCE-PRECISION",
		Type:      service.RedeemTypeBalance,
		Value:     1.23456789,
		GiftRatio: 12.3456,
		GiftValue: 0.15241579,
		Status:    service.StatusUnused,
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/export", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "value", records[0][3])
	require.Equal(t, "gift_ratio", records[0][4])
	require.Equal(t, "gift_value", records[0][5])
	require.Equal(t, "1.23456789", records[1][3])
	require.Equal(t, "12.3456", records[1][4])
	require.Equal(t, "0.15241579", records[1][5])
}

func TestRedeemExportIncludesFrozenQuotaAndDuration(t *testing.T) {
	router, adminSvc := setupRedeemExportRouter()
	adminSvc.redeems = []service.RedeemCode{
		{ID: 1, Code: "FINITE", Type: service.RedeemTypeSubscription, GroupID: new(int64(7)), QuotaAmount: new(42.12345678), ValidityDays: 14},
		{ID: 2, Code: "PERMANENT", Type: service.RedeemTypeSubscription, GroupID: new(int64(8)), QuotaAmount: new(7.5), ValidityDays: 0},
		{ID: 3, Code: "LEGACY", Type: service.RedeemTypeSubscription, GroupID: new(int64(9)), ValidityDays: 30},
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/export", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 4)
	require.Equal(t, []string{"quota_amount", "group_id", "validity_days", "total_quota_no_expiry"}, records[0][12:])
	require.Equal(t, []string{"42.12345678", "7", "14", "false"}, records[1][12:])
	require.Equal(t, []string{"7.50000000", "8", "0", "true"}, records[2][12:])
	require.Equal(t, []string{"", "9", "30", ""}, records[3][12:])
}
