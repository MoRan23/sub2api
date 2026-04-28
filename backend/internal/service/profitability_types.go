package service

import (
	"context"
	"time"
)

const (
	defaultProfitabilityFXRateUSDCNY   = 7.20
	defaultProfitabilityTargetMargin   = 0.25
	defaultProfitabilitySmoothingAlpha = 0.60
)

type ProfitabilityPrecision string

const (
	ProfitabilityPrecisionExact     ProfitabilityPrecision = "exact"
	ProfitabilityPrecisionEstimated ProfitabilityPrecision = "estimated"
	ProfitabilityPrecisionMixed     ProfitabilityPrecision = "mixed"
	ProfitabilityPrecisionDerived   ProfitabilityPrecision = "derived"
)

type ProfitabilityConfig struct {
	FXRateUSDCNY   float64 `json:"fx_rate_usd_cny"`
	TargetMargin   float64 `json:"target_margin"`
	SmoothingAlpha float64 `json:"smoothing_alpha"`
}

type ProfitabilityQuery struct {
	StartTime   time.Time
	EndTime     time.Time
	Granularity string
	PlanID      int64
	GroupID     int64
	UserID      int64
	AccountID   int64
}

type ProfitabilityMeta struct {
	Precision   ProfitabilityPrecision `json:"precision"`
	Description string                 `json:"description"`
}

type ProfitabilityMetaBundle struct {
	Summary      ProfitabilityMeta `json:"summary"`
	Trends       ProfitabilityMeta `json:"trends"`
	Plans        ProfitabilityMeta `json:"plans"`
	Groups       ProfitabilityMeta `json:"groups"`
	Users        ProfitabilityMeta `json:"users"`
	Accounts     ProfitabilityMeta `json:"accounts"`
	Optimization ProfitabilityMeta `json:"optimization"`
	Pricing      ProfitabilityMeta `json:"pricing"`
}

type ProfitabilityPrecisionNote struct {
	Key         string                 `json:"key"`
	Label       string                 `json:"label"`
	Precision   ProfitabilityPrecision `json:"precision"`
	Description string                 `json:"description"`
}

type ProfitabilitySummary struct {
	RevenueRMB              float64                `json:"revenue_rmb"`
	RevenuePrecision        ProfitabilityPrecision `json:"revenue_precision"`
	UsageProxyUSD           float64                `json:"usage_proxy_usd"`
	AccountCostUSD          float64                `json:"account_cost_usd"`
	StandardCostUSD         float64                `json:"standard_cost_usd"`
	ProfitRMB               float64                `json:"profit_rmb"`
	ProfitUSD               float64                `json:"profit_usd"`
	ProfitMarginPercent     *float64               `json:"profit_margin_percent,omitempty"`
	Remaining5hUSD          float64                `json:"remaining_5h_usd"`
	Remaining7dUSD          float64                `json:"remaining_7d_usd"`
	EstimatedRunoutHours    *float64               `json:"estimated_runout_hours,omitempty"`
	EstimatedRunoutAt       *time.Time             `json:"estimated_runout_at,omitempty"`
	ActiveAccountsWithQuota int                    `json:"active_accounts_with_quota"`
}

type ProfitabilityTrendPoint struct {
	Bucket              time.Time               `json:"bucket"`
	RevenueRMB          float64                 `json:"revenue_rmb"`
	RevenuePrecision    ProfitabilityPrecision  `json:"revenue_precision"`
	UsageProxyUSD       float64                 `json:"usage_proxy_usd"`
	AccountCostUSD      float64                 `json:"account_cost_usd"`
	StandardCostUSD     float64                 `json:"standard_cost_usd"`
	ProfitRMB           float64                 `json:"profit_rmb"`
	ProfitMarginPercent *float64                `json:"profit_margin_percent,omitempty"`
}

type ProfitabilityPlanItem struct {
	PlanID                    int64                  `json:"plan_id"`
	GroupID                   int64                  `json:"group_id"`
	GroupName                 string                 `json:"group_name"`
	GroupPlatform             string                 `json:"group_platform"`
	Name                      string                 `json:"name"`
	PriceRMB                  float64                `json:"price_rmb"`
	OriginalPriceRMB          *float64               `json:"original_price_rmb,omitempty"`
	ValidityDays              int                    `json:"validity_days"`
	SoldCount                 int                    `json:"sold_count"`
	ActiveSalesEstimate       int                    `json:"active_sales_estimate"`
	RecognizedRevenueRMB      float64                `json:"recognized_revenue_rmb"`
	EstimatedUsageProxyUSD    float64                `json:"estimated_usage_proxy_usd"`
	EstimatedAccountCostUSD   float64                `json:"estimated_account_cost_usd"`
	EstimatedStandardCostUSD  float64                `json:"estimated_standard_cost_usd"`
	EstimatedProfitRMB        float64                `json:"estimated_profit_rmb"`
	ProfitMarginPercent       *float64               `json:"profit_margin_percent,omitempty"`
	AllocationSoldDays        float64                `json:"allocation_sold_days"`
	Precision                 ProfitabilityPrecision `json:"precision"`
}

type ProfitabilityGroupItem struct {
	GroupID                  int64                  `json:"group_id"`
	GroupName                string                 `json:"group_name"`
	GroupPlatform            string                 `json:"group_platform"`
	SubscriptionType         string                 `json:"subscription_type"`
	RecognizedRevenueRMB     float64                `json:"recognized_revenue_rmb"`
	UsageProxyUSD            float64                `json:"usage_proxy_usd"`
	AccountCostUSD           float64                `json:"account_cost_usd"`
	StandardCostUSD          float64                `json:"standard_cost_usd"`
	ProfitRMB                float64                `json:"profit_rmb"`
	ProfitMarginPercent      *float64               `json:"profit_margin_percent,omitempty"`
	MarginalContributionRMB  float64                `json:"marginal_contribution_rmb"`
	SensitivityDeltaProfitRMB float64               `json:"sensitivity_delta_profit_rmb"`
	Precision                ProfitabilityPrecision `json:"precision"`
}

type ProfitabilityUserItem struct {
	UserID                  int64                  `json:"user_id"`
	Email                   string                 `json:"email"`
	GroupID                 int64                  `json:"group_id"`
	GroupName               string                 `json:"group_name"`
	SubscriptionID          int64                  `json:"subscription_id"`
	SubscriptionType        string                 `json:"subscription_type"`
	RecognizedRevenueRMB    float64                `json:"recognized_revenue_rmb"`
	RevenuePrecision        ProfitabilityPrecision `json:"revenue_precision"`
	UsageProxyUSD           float64                `json:"usage_proxy_usd"`
	AccountCostUSD          float64                `json:"account_cost_usd"`
	StandardCostUSD         float64                `json:"standard_cost_usd"`
	EstimatedProfitRMB      float64                `json:"estimated_profit_rmb"`
	RemainingQuotaUSD       *float64               `json:"remaining_quota_usd,omitempty"`
	BurnRate5hUSDPerHour    float64                `json:"burn_rate_5h_usd_per_hour"`
	BurnRate24hUSDPerHour   float64                `json:"burn_rate_24h_usd_per_hour"`
	BurnRate7dUSDPerHour    float64                `json:"burn_rate_7d_usd_per_hour"`
	ForecastBurnUSDPerHour  float64                `json:"forecast_burn_usd_per_hour"`
	RunwayHours             *float64               `json:"runway_hours,omitempty"`
	VolatilityUSD           float64                `json:"volatility_usd"`
	RiskAdjustedProfitUSD   *float64               `json:"risk_adjusted_profit_usd,omitempty"`
	RiskLevel               string                 `json:"risk_level"`
	Precision               ProfitabilityPrecision `json:"precision"`
}

type ProfitabilityAccountRiskItem struct {
	AccountID                int64                  `json:"account_id"`
	Name                     string                 `json:"name"`
	Platform                 string                 `json:"platform"`
	Type                     string                 `json:"type"`
	GroupIDs                 []int64                `json:"group_ids,omitempty"`
	GroupNames               []string               `json:"group_names,omitempty"`
	ProfitValueCNY           *float64               `json:"profit_value_cny,omitempty"`
	ProfitCapacityUSD5h      *float64               `json:"profit_capacity_usd_5h,omitempty"`
	ProfitCapacityUSD7d      *float64               `json:"profit_capacity_usd_7d,omitempty"`
	ObservedCost5hUSD        float64                `json:"observed_cost_5h_usd"`
	ObservedCost24hUSD       float64                `json:"observed_cost_24h_usd"`
	ObservedCost7dUSD        float64                `json:"observed_cost_7d_usd"`
	UsedPercent5h            *float64               `json:"used_percent_5h,omitempty"`
	UsedPercent7d            *float64               `json:"used_percent_7d,omitempty"`
	Remaining5hUSD           *float64               `json:"remaining_5h_usd,omitempty"`
	Remaining7dUSD           *float64               `json:"remaining_7d_usd,omitempty"`
	BurnRate5hUSDPerHour     float64                `json:"burn_rate_5h_usd_per_hour"`
	BurnRate24hUSDPerHour    float64                `json:"burn_rate_24h_usd_per_hour"`
	BurnRate7dUSDPerHour     float64                `json:"burn_rate_7d_usd_per_hour"`
	ForecastBurnUSDPerHour   float64                `json:"forecast_burn_usd_per_hour"`
	Runway5hHours            *float64               `json:"runway_5h_hours,omitempty"`
	Runway7dHours            *float64               `json:"runway_7d_hours,omitempty"`
	PeriodUsageProxyUSD      float64                `json:"period_usage_proxy_usd"`
	PeriodAccountCostUSD     float64                `json:"period_account_cost_usd"`
	PeriodStandardCostUSD    float64                `json:"period_standard_cost_usd"`
	VolatilityUSD            float64                `json:"volatility_usd"`
	RiskAdjustedProfitUSD    *float64               `json:"risk_adjusted_profit_usd,omitempty"`
	RiskLevel                string                 `json:"risk_level"`
	Precision                ProfitabilityPrecision `json:"precision"`
}

type ProfitabilityConstraint struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Capacity  float64 `json:"capacity"`
	Used      float64 `json:"used"`
	Remaining float64 `json:"remaining"`
}

type ProfitabilityOptimizationPlan struct {
	PlanID                        int64                     `json:"plan_id"`
	Name                          string                    `json:"name"`
	GroupID                       int64                     `json:"group_id"`
	GroupName                     string                    `json:"group_name"`
	RecommendedAdditionalSales    float64                   `json:"recommended_additional_sales"`
	EstimatedIncrementalRevenueRMB float64                  `json:"estimated_incremental_revenue_rmb"`
	EstimatedIncrementalCostUSD   float64                   `json:"estimated_incremental_cost_usd"`
	EstimatedIncrementalProfitRMB float64                   `json:"estimated_incremental_profit_rmb"`
	BindingConstraints            []ProfitabilityConstraint `json:"binding_constraints,omitempty"`
}

type ProfitabilitySensitivityScenario struct {
	Key             string  `json:"key"`
	Label           string  `json:"label"`
	RevenueDeltaRMB float64 `json:"revenue_delta_rmb"`
	CostDeltaUSD    float64 `json:"cost_delta_usd"`
	ProfitDeltaRMB  float64 `json:"profit_delta_rmb"`
}

type ProfitabilityOptimizationResult struct {
	Objective                        string                          `json:"objective"`
	EstimatedIncrementalProfitRMB    float64                         `json:"estimated_incremental_profit_rmb"`
	EstimatedIncrementalRevenueRMB   float64                         `json:"estimated_incremental_revenue_rmb"`
	EstimatedIncrementalCostUSD      float64                         `json:"estimated_incremental_cost_usd"`
	Plans                            []ProfitabilityOptimizationPlan `json:"plans"`
	Bottlenecks                      []string                        `json:"bottlenecks"`
	SensitivityScenarios             []ProfitabilitySensitivityScenario `json:"sensitivity_scenarios"`
	Precision                        ProfitabilityPrecision          `json:"precision"`
}

type ProfitabilityPricingRecommendation struct {
	PlanID                int64                  `json:"plan_id"`
	Name                  string                 `json:"name"`
	GroupID               int64                  `json:"group_id"`
	GroupName             string                 `json:"group_name"`
	CurrentPriceRMB       float64                `json:"current_price_rmb"`
	RecommendedPriceRMB   float64                `json:"recommended_price_rmb"`
	TargetMarginPercent   float64                `json:"target_margin_percent"`
	RiskPremiumPercent    float64                `json:"risk_premium_percent"`
	UnitEstimatedCostUSD  float64                `json:"unit_estimated_cost_usd"`
	EstimatedProfitRMB    float64                `json:"estimated_profit_rmb"`
	Reason                string                 `json:"reason"`
	Precision             ProfitabilityPrecision `json:"precision"`
}

type ProfitabilitySnapshot struct {
	GeneratedAt            time.Time                            `json:"generated_at"`
	StartDate              string                               `json:"start_date"`
	EndDate                string                               `json:"end_date"`
	AppliedFilters         ProfitabilityQuery                   `json:"applied_filters"`
	Config                 ProfitabilityConfig                  `json:"config"`
	Meta                   ProfitabilityMetaBundle              `json:"meta"`
	PrecisionNotes         []ProfitabilityPrecisionNote         `json:"precision_notes"`
	Summary                ProfitabilitySummary                 `json:"summary"`
	Trends                 []ProfitabilityTrendPoint            `json:"trends"`
	Plans                  []ProfitabilityPlanItem             `json:"plans"`
	Groups                 []ProfitabilityGroupItem            `json:"groups"`
	Users                  []ProfitabilityUserItem             `json:"users"`
	Accounts               []ProfitabilityAccountRiskItem      `json:"accounts"`
	Optimization           ProfitabilityOptimizationResult     `json:"optimization"`
	PricingRecommendations []ProfitabilityPricingRecommendation `json:"pricing_recommendations"`
}

type ProfitabilityAccountValueUpdate struct {
	AccountID           int64    `json:"account_id"`
	ProfitValueCNY      *float64 `json:"profit_value_cny,omitempty"`
	ProfitCapacityUSD5h *float64 `json:"profit_capacity_usd_5h,omitempty"`
	ProfitCapacityUSD7d *float64 `json:"profit_capacity_usd_7d,omitempty"`
}

type ProfitabilityUsageFilter struct {
	StartTime time.Time
	EndTime   time.Time
	UserID    int64
	GroupID   int64
	AccountID int64
}

type ProfitabilityUsageTrendRow struct {
	Bucket          time.Time
	StandardCostUSD float64
	UsageProxyUSD   float64
	AccountCostUSD  float64
}

type ProfitabilityAccountAggregateRow struct {
	AccountID        int64
	StandardCostUSD  float64
	UsageProxyUSD    float64
	AccountCostUSD   float64
}

type ProfitabilityAccountCostPoint struct {
	AccountID      int64
	Bucket         time.Time
	AccountCostUSD float64
}

type ProfitabilityUserAggregateRow struct {
	UserID         int64
	Email          string
	GroupID        int64
	SubscriptionID int64
	StandardCostUSD float64
	UsageProxyUSD   float64
	AccountCostUSD  float64
}

type ProfitabilitySubscriptionWindowUsageRow struct {
	SubscriptionID  int64
	UsageProxyUSD   float64
	AccountCostUSD  float64
}

type ProfitabilityUserCostPoint struct {
	SubscriptionID int64
	Bucket         time.Time
	UsageProxyUSD  float64
}

type ProfitabilityUsageRepository interface {
	GetUsageCostTrend(ctx context.Context, filter ProfitabilityUsageFilter, granularity string) ([]ProfitabilityUsageTrendRow, error)
	GetAccountAggregates(ctx context.Context, filter ProfitabilityUsageFilter) ([]ProfitabilityAccountAggregateRow, error)
	GetAccountWindowAggregates(ctx context.Context, filter ProfitabilityUsageFilter, startTime time.Time) (map[int64]float64, error)
	GetAccountCostSeries(ctx context.Context, filter ProfitabilityUsageFilter, startTime, endTime time.Time, granularity string) ([]ProfitabilityAccountCostPoint, error)
	GetUserAggregates(ctx context.Context, filter ProfitabilityUsageFilter) ([]ProfitabilityUserAggregateRow, error)
	GetUserWindowAggregates(ctx context.Context, filter ProfitabilityUsageFilter, startTime time.Time) (map[int64]ProfitabilitySubscriptionWindowUsageRow, error)
	GetUserCostSeries(ctx context.Context, filter ProfitabilityUsageFilter, startTime, endTime time.Time, granularity string) ([]ProfitabilityUserCostPoint, error)
}
