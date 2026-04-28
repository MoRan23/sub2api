package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func parseProfitabilityQuery(c *gin.Context) service.ProfitabilityQuery {
	startTime, endTime := parseTimeRange(c)
	query := service.ProfitabilityQuery{
		StartTime:   startTime,
		EndTime:     endTime,
		Granularity: c.DefaultQuery("granularity", "day"),
	}
	if planID, err := strconv.ParseInt(strings.TrimSpace(c.Query("plan_id")), 10, 64); err == nil && planID > 0 {
		query.PlanID = planID
	}
	if groupID, err := strconv.ParseInt(strings.TrimSpace(c.Query("group_id")), 10, 64); err == nil && groupID > 0 {
		query.GroupID = groupID
	}
	if userID, err := strconv.ParseInt(strings.TrimSpace(c.Query("user_id")), 10, 64); err == nil && userID > 0 {
		query.UserID = userID
	}
	if accountID, err := strconv.ParseInt(strings.TrimSpace(c.Query("account_id")), 10, 64); err == nil && accountID > 0 {
		query.AccountID = accountID
	}
	return query
}

// GetProfitabilitySnapshot handles GET /api/v1/admin/dashboard/profitability/snapshot
func (h *DashboardHandler) GetProfitabilitySnapshot(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	snapshot, err := h.profitabilityService.GetSnapshot(c.Request.Context(), parseProfitabilityQuery(c))
	if err != nil {
		response.Error(c, 500, "Failed to get profitability snapshot: "+err.Error())
		return
	}
	response.Success(c, snapshot)
}

// GetProfitabilityPlans handles GET /api/v1/admin/dashboard/profitability/plans
func (h *DashboardHandler) GetProfitabilityPlans(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	items, err := h.profitabilityService.GetPlans(c.Request.Context(), parseProfitabilityQuery(c))
	if err != nil {
		response.Error(c, 500, "Failed to get profitability plans: "+err.Error())
		return
	}
	response.Success(c, gin.H{"items": items})
}

// GetProfitabilityGroups handles GET /api/v1/admin/dashboard/profitability/groups
func (h *DashboardHandler) GetProfitabilityGroups(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	items, err := h.profitabilityService.GetGroups(c.Request.Context(), parseProfitabilityQuery(c))
	if err != nil {
		response.Error(c, 500, "Failed to get profitability groups: "+err.Error())
		return
	}
	response.Success(c, gin.H{"items": items})
}

// GetProfitabilityUsers handles GET /api/v1/admin/dashboard/profitability/users
func (h *DashboardHandler) GetProfitabilityUsers(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	items, err := h.profitabilityService.GetUsers(c.Request.Context(), parseProfitabilityQuery(c))
	if err != nil {
		response.Error(c, 500, "Failed to get profitability users: "+err.Error())
		return
	}
	response.Success(c, gin.H{"items": items})
}

// GetProfitabilityAccountRisk handles GET /api/v1/admin/dashboard/profitability/accounts/risk
func (h *DashboardHandler) GetProfitabilityAccountRisk(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	items, err := h.profitabilityService.GetAccountRisks(c.Request.Context(), parseProfitabilityQuery(c))
	if err != nil {
		response.Error(c, 500, "Failed to get profitability account risk: "+err.Error())
		return
	}
	response.Success(c, gin.H{"items": items})
}

// GetProfitabilityOptimization handles GET /api/v1/admin/dashboard/profitability/optimization
func (h *DashboardHandler) GetProfitabilityOptimization(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	result, err := h.profitabilityService.GetOptimization(c.Request.Context(), parseProfitabilityQuery(c))
	if err != nil {
		response.Error(c, 500, "Failed to get profitability optimization: "+err.Error())
		return
	}
	response.Success(c, result)
}

// GetProfitabilityPricingRecommendations handles GET /api/v1/admin/dashboard/profitability/pricing-recommendations
func (h *DashboardHandler) GetProfitabilityPricingRecommendations(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	items, err := h.profitabilityService.GetPricingRecommendations(c.Request.Context(), parseProfitabilityQuery(c))
	if err != nil {
		response.Error(c, 500, "Failed to get profitability pricing recommendations: "+err.Error())
		return
	}
	response.Success(c, gin.H{"items": items})
}

// GetProfitabilityConfig handles GET /api/v1/admin/dashboard/profitability/config
func (h *DashboardHandler) GetProfitabilityConfig(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	response.Success(c, h.profitabilityService.GetConfig(c.Request.Context()))
}

// UpdateProfitabilityConfig handles PUT /api/v1/admin/dashboard/profitability/config
func (h *DashboardHandler) UpdateProfitabilityConfig(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	var req service.ProfitabilityConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.profitabilityService.UpdateConfig(c.Request.Context(), req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, req)
}

type batchProfitabilityAccountValueRequest struct {
	Items []service.ProfitabilityAccountValueUpdate `json:"items" binding:"required"`
}

// UpdateProfitabilityAccountValues handles POST /api/v1/admin/dashboard/profitability/account-values/batch
func (h *DashboardHandler) UpdateProfitabilityAccountValues(c *gin.Context) {
	if h == nil || h.profitabilityService == nil {
		response.InternalError(c, "Profitability service is not available")
		return
	}
	var req batchProfitabilityAccountValueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.profitabilityService.UpdateAccountValues(c.Request.Context(), req.Items); err != nil {
		response.Error(c, 500, "Failed to update profitability account values: "+err.Error())
		return
	}
	response.Success(c, gin.H{"updated": len(req.Items)})
}
