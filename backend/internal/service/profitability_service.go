package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	entpaymentorder "github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const (
	profitabilitySeriesDays = 8
	profitabilityTopN       = 12
)

type ProfitabilityService struct {
	repo                 ProfitabilityUsageRepository
	accountRepo          AccountRepository
	userSubRepo          UserSubscriptionRepository
	dashboardService     *DashboardService
	paymentConfigService *PaymentConfigService
	settingService       *SettingService
	entClient            *dbent.Client
}

func NewProfitabilityService(
	repo ProfitabilityUsageRepository,
	accountRepo AccountRepository,
	userSubRepo UserSubscriptionRepository,
	dashboardService *DashboardService,
	paymentConfigService *PaymentConfigService,
	settingService *SettingService,
	entClient *dbent.Client,
) *ProfitabilityService {
	return &ProfitabilityService{
		repo:                 repo,
		accountRepo:          accountRepo,
		userSubRepo:          userSubRepo,
		dashboardService:     dashboardService,
		paymentConfigService: paymentConfigService,
		settingService:       settingService,
		entClient:            entClient,
	}
}

func (s *ProfitabilityService) GetConfig(ctx context.Context) ProfitabilityConfig {
	if s == nil || s.settingService == nil {
		return ProfitabilityConfig{
			FXRateUSDCNY:   defaultProfitabilityFXRateUSDCNY,
			TargetMargin:   defaultProfitabilityTargetMargin,
			SmoothingAlpha: defaultProfitabilitySmoothingAlpha,
		}
	}
	return s.settingService.GetProfitabilityConfig(ctx)
}

func (s *ProfitabilityService) UpdateConfig(ctx context.Context, cfg ProfitabilityConfig) error {
	if s == nil || s.settingService == nil {
		return fmt.Errorf("profitability setting service unavailable")
	}
	return s.settingService.UpdateProfitabilityConfig(ctx, cfg)
}

func (s *ProfitabilityService) UpdateAccountValues(ctx context.Context, updates []ProfitabilityAccountValueUpdate) error {
	if s == nil || s.accountRepo == nil {
		return fmt.Errorf("profitability account repository unavailable")
	}
	for _, update := range updates {
		if update.AccountID <= 0 {
			continue
		}
		extra := map[string]any{}
		if update.ProfitValueCNY != nil {
			extra["profit_value_cny"] = profitabilityMaxFloat(0, *update.ProfitValueCNY)
		}
		if update.ProfitCapacityUSD5h != nil {
			extra["profit_capacity_usd_5h"] = profitabilityMaxFloat(0, *update.ProfitCapacityUSD5h)
		}
		if update.ProfitCapacityUSD7d != nil {
			extra["profit_capacity_usd_7d"] = profitabilityMaxFloat(0, *update.ProfitCapacityUSD7d)
		}
		if len(extra) == 0 {
			continue
		}
		if err := s.accountRepo.UpdateExtra(ctx, update.AccountID, extra); err != nil {
			return err
		}
	}
	return nil
}

func (s *ProfitabilityService) GetSnapshot(ctx context.Context, query ProfitabilityQuery) (*ProfitabilitySnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("profitability service is nil")
	}

	query = normalizeProfitabilityQuery(query)
	cfg := s.GetConfig(ctx)

	plans, err := s.loadPlans(ctx, query)
	if err != nil {
		return nil, err
	}
	plansByID := make(map[int64]*dbent.SubscriptionPlan, len(plans))
	for i := range plans {
		plan := plans[i]
		plansByID[plan.ID] = plan
	}

	selectedPlan := plansByID[query.PlanID]
	if query.PlanID > 0 && selectedPlan == nil {
		if plan, planErr := s.paymentConfigService.GetPlan(ctx, query.PlanID); planErr == nil {
			selectedPlan = plan
			plansByID[plan.ID] = plan
			plans = append(plans, plan)
		}
	}

	if selectedPlan != nil {
		query.GroupID = selectedPlan.GroupID
	}

	usageFilter := ProfitabilityUsageFilter{
		StartTime: query.StartTime,
		EndTime:   query.EndTime,
		UserID:    query.UserID,
		GroupID:   query.GroupID,
		AccountID: query.AccountID,
	}

	now := time.Now().UTC()
	revenueMode := resolveProfitabilityRevenueMode(query)

	orders, err := s.loadSubscriptionOrders(ctx, query, plans)
	if err != nil {
		return nil, err
	}

	orderStats := buildProfitabilityOrderStats(orders, query.StartTime, query.EndTime, now, query.Granularity, plansByID)
	groupPlanRatios := buildGroupPlanRatios(orderStats.PlanSoldDaysByGroup)

	groupRows, err := s.dashboardService.GetGroupStatsWithFilters(ctx, query.StartTime, query.EndTime, usageFilter.UserID, 0, usageFilter.AccountID, usageFilter.GroupID, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get profitability groups: %w", err)
	}
	groupStatsByID := make(map[int64]usagestats.GroupStat, len(groupRows))
	for _, row := range groupRows {
		if query.PlanID > 0 {
			if ratio := groupPlanRatios[row.GroupID][query.PlanID]; ratio > 0 {
				row.ActualCost *= ratio
				row.AccountCost *= ratio
				row.Cost *= ratio
			} else {
				row.ActualCost = 0
				row.AccountCost = 0
				row.Cost = 0
			}
		}
		groupStatsByID[row.GroupID] = row
	}

	trendRows, err := s.repo.GetUsageCostTrend(ctx, usageFilter, query.Granularity)
	if err != nil {
		return nil, fmt.Errorf("get profitability trends: %w", err)
	}
	if query.PlanID > 0 {
		ratio := groupPlanRatios[query.GroupID][query.PlanID]
		for i := range trendRows {
			trendRows[i].UsageProxyUSD *= ratio
			trendRows[i].AccountCostUSD *= ratio
			trendRows[i].StandardCostUSD *= ratio
		}
	}

	accounts, err := s.listAccounts(ctx, query)
	if err != nil {
		return nil, err
	}
	accountItems, accountSummary, err := s.buildAccountRiskItems(ctx, query, usageFilter, cfg, accounts, groupPlanRatios)
	if err != nil {
		return nil, err
	}

	userItems, err := s.buildUserItems(ctx, query, usageFilter, cfg, orderStats, groupPlanRatios)
	if err != nil {
		return nil, err
	}

	groupItems := s.buildGroupItems(query, ctx, cfg, groupRows, orderStats, plans, revenueMode)
	planItems := s.buildPlanItems(ctx, query, cfg, plans, groupStatsByID, orderStats, groupPlanRatios, revenueMode, now)
	trends := s.buildTrendPoints(query, cfg, trendRows, orderStats.RevenueTrendByBucket, revenueMode)
	summary := s.buildSummary(cfg, trends, accountSummary, revenueMode)
	optimization := s.buildOptimization(cfg, planItems, accountItems, groupItems)
	pricing := s.buildPricingRecommendations(cfg, planItems, accountItems)

	snapshot := &ProfitabilitySnapshot{
		GeneratedAt:            now,
		StartDate:              query.StartTime.Format("2006-01-02"),
		EndDate:                query.EndTime.Add(-time.Second).Format("2006-01-02"),
		AppliedFilters:         query,
		Config:                 cfg,
		Meta:                   profitabilityMetaBundle(revenueMode),
		PrecisionNotes:         profitabilityPrecisionNotes(revenueMode),
		Summary:                summary,
		Trends:                 trends,
		Plans:                  trimProfitabilityPlans(planItems, profitabilityTopN),
		Groups:                 trimProfitabilityGroups(groupItems, profitabilityTopN),
		Users:                  trimProfitabilityUsers(userItems, profitabilityTopN),
		Accounts:               trimProfitabilityAccounts(accountItems, profitabilityTopN),
		Optimization:           optimization,
		PricingRecommendations: trimProfitabilityPricing(pricing, profitabilityTopN),
	}
	return snapshot, nil
}

func (s *ProfitabilityService) GetPlans(ctx context.Context, query ProfitabilityQuery) ([]ProfitabilityPlanItem, error) {
	snapshot, err := s.GetSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return snapshot.Plans, nil
}

func (s *ProfitabilityService) GetGroups(ctx context.Context, query ProfitabilityQuery) ([]ProfitabilityGroupItem, error) {
	snapshot, err := s.GetSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return snapshot.Groups, nil
}

func (s *ProfitabilityService) GetUsers(ctx context.Context, query ProfitabilityQuery) ([]ProfitabilityUserItem, error) {
	snapshot, err := s.GetSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return snapshot.Users, nil
}

func (s *ProfitabilityService) GetAccountRisks(ctx context.Context, query ProfitabilityQuery) ([]ProfitabilityAccountRiskItem, error) {
	snapshot, err := s.GetSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return snapshot.Accounts, nil
}

func (s *ProfitabilityService) GetOptimization(ctx context.Context, query ProfitabilityQuery) (*ProfitabilityOptimizationResult, error) {
	snapshot, err := s.GetSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return &snapshot.Optimization, nil
}

func (s *ProfitabilityService) GetPricingRecommendations(ctx context.Context, query ProfitabilityQuery) ([]ProfitabilityPricingRecommendation, error) {
	snapshot, err := s.GetSnapshot(ctx, query)
	if err != nil {
		return nil, err
	}
	return snapshot.PricingRecommendations, nil
}

func (s *ProfitabilityService) loadPlans(ctx context.Context, query ProfitabilityQuery) ([]*dbent.SubscriptionPlan, error) {
	plans, err := s.paymentConfigService.ListPlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("list profitability plans: %w", err)
	}
	if query.GroupID <= 0 && query.PlanID <= 0 {
		return plans, nil
	}
	filtered := make([]*dbent.SubscriptionPlan, 0, len(plans))
	for _, plan := range plans {
		if query.PlanID > 0 && plan.ID != query.PlanID {
			continue
		}
		if query.GroupID > 0 && plan.GroupID != query.GroupID {
			continue
		}
		filtered = append(filtered, plan)
	}
	return filtered, nil
}

func (s *ProfitabilityService) loadSubscriptionOrders(ctx context.Context, query ProfitabilityQuery, plans []*dbent.SubscriptionPlan) ([]*dbent.PaymentOrder, error) {
	if s.entClient == nil {
		return nil, fmt.Errorf("profitability ent client unavailable")
	}
	maxValidityDays := 365
	for _, plan := range plans {
		if plan != nil && plan.ValidityDays > maxValidityDays {
			maxValidityDays = plan.ValidityDays
		}
	}
	lowerBound := query.StartTime.Add(-time.Duration(maxValidityDays) * 24 * time.Hour)

	builder := s.entClient.PaymentOrder.Query().
		Where(
			entpaymentorder.OrderTypeEQ(payment.OrderTypeSubscription),
			entpaymentorder.PlanIDNotNil(),
			entpaymentorder.SubscriptionGroupIDNotNil(),
			entpaymentorder.PaidAtNotNil(),
			entpaymentorder.PaidAtGTE(lowerBound),
			entpaymentorder.PaidAtLT(query.EndTime),
			entpaymentorder.StatusNotIn(OrderStatusPending, OrderStatusCancelled, OrderStatusFailed, OrderStatusExpired),
		)

	if query.PlanID > 0 {
		builder = builder.Where(entpaymentorder.PlanIDEQ(query.PlanID))
	}
	if query.GroupID > 0 {
		builder = builder.Where(entpaymentorder.SubscriptionGroupIDEQ(query.GroupID))
	}
	if query.UserID > 0 {
		builder = builder.Where(entpaymentorder.UserIDEQ(query.UserID))
	}

	orders, err := builder.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query profitability orders: %w", err)
	}
	return orders, nil
}

func (s *ProfitabilityService) buildGroupItems(
	query ProfitabilityQuery,
	ctx context.Context,
	cfg ProfitabilityConfig,
	rows []usagestats.GroupStat,
	orderStats profitabilityOrderStats,
	plans []*dbent.SubscriptionPlan,
	mode profitabilityRevenueMode,
) []ProfitabilityGroupItem {
	groupInfo := s.paymentConfigService.GetGroupInfoMap(ctx, plans)
	items := make([]ProfitabilityGroupItem, 0, len(rows))
	totalProfitRMB := 0.0
	for _, row := range rows {
		revenueRMB := profitabilityRevenueForGroup(row.GroupID, row.ActualCost, cfg.FXRateUSDCNY, orderStats, mode)
		profitRMB := revenueRMB - row.AccountCost*cfg.FXRateUSDCNY
		margin := nullableMargin(profitRMB, revenueRMB)
		item := ProfitabilityGroupItem{
			GroupID:                   row.GroupID,
			GroupName:                 row.GroupName,
			GroupPlatform:             groupInfo[row.GroupID].Platform,
			SubscriptionType:          groupInfo[row.GroupID].SubscriptionType,
			RecognizedRevenueRMB:      revenueRMB,
			UsageProxyUSD:             row.ActualCost,
			AccountCostUSD:            row.AccountCost,
			StandardCostUSD:           row.Cost,
			ProfitRMB:                 profitRMB,
			ProfitMarginPercent:       margin,
			MarginalContributionRMB:   profitRMB,
			Precision:                 precisionForRevenueMode(mode),
			SensitivityDeltaProfitRMB: 0,
		}
		items = append(items, item)
		totalProfitRMB += profitRMB
	}
	for i := range items {
		items[i].SensitivityDeltaProfitRMB = -items[i].ProfitRMB
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ProfitRMB == items[j].ProfitRMB {
			return items[i].GroupID < items[j].GroupID
		}
		return items[i].ProfitRMB > items[j].ProfitRMB
	})
	_ = totalProfitRMB
	return items
}

func (s *ProfitabilityService) buildPlanItems(
	ctx context.Context,
	query ProfitabilityQuery,
	cfg ProfitabilityConfig,
	plans []*dbent.SubscriptionPlan,
	groupStats map[int64]usagestats.GroupStat,
	orderStats profitabilityOrderStats,
	groupPlanRatios map[int64]map[int64]float64,
	mode profitabilityRevenueMode,
	now time.Time,
) []ProfitabilityPlanItem {
	groupInfo := s.paymentConfigService.GetGroupInfoMap(ctx, plans)
	items := make([]ProfitabilityPlanItem, 0, len(plans))
	for _, plan := range plans {
		if query.PlanID > 0 && plan.ID != query.PlanID {
			continue
		}
		if query.GroupID > 0 && plan.GroupID != query.GroupID {
			continue
		}
		ratio := groupPlanRatios[plan.GroupID][plan.ID]
		stat := groupStats[plan.GroupID]
		revenueRMB := profitabilityRevenueForPlan(plan.ID, stat.ActualCost*ratio, cfg.FXRateUSDCNY, orderStats, mode)
		estimatedUsage := stat.ActualCost * ratio
		estimatedAccountCost := stat.AccountCost * ratio
		estimatedStandardCost := stat.Cost * ratio
		profitRMB := revenueRMB - estimatedAccountCost*cfg.FXRateUSDCNY
		items = append(items, ProfitabilityPlanItem{
			PlanID:                   plan.ID,
			GroupID:                  plan.GroupID,
			GroupName:                groupInfo[plan.GroupID].Name,
			GroupPlatform:            groupInfo[plan.GroupID].Platform,
			Name:                     plan.Name,
			PriceRMB:                 plan.Price,
			OriginalPriceRMB:         plan.OriginalPrice,
			ValidityDays:             plan.ValidityDays,
			SoldCount:                orderStats.PlanOrderCount[plan.ID],
			ActiveSalesEstimate:      orderStats.PlanActiveUnits[plan.ID],
			RecognizedRevenueRMB:     revenueRMB,
			EstimatedUsageProxyUSD:   estimatedUsage,
			EstimatedAccountCostUSD:  estimatedAccountCost,
			EstimatedStandardCostUSD: estimatedStandardCost,
			EstimatedProfitRMB:       profitRMB,
			ProfitMarginPercent:      nullableMargin(profitRMB, revenueRMB),
			AllocationSoldDays:       orderStats.PlanSoldDaysByGroup[plan.GroupID][plan.ID],
			Precision:                planPrecisionForMode(mode),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].EstimatedProfitRMB == items[j].EstimatedProfitRMB {
			return items[i].PlanID < items[j].PlanID
		}
		return items[i].EstimatedProfitRMB > items[j].EstimatedProfitRMB
	})
	_ = now
	return items
}

func (s *ProfitabilityService) buildUserItems(
	ctx context.Context,
	query ProfitabilityQuery,
	filter ProfitabilityUsageFilter,
	cfg ProfitabilityConfig,
	orderStats profitabilityOrderStats,
	groupPlanRatios map[int64]map[int64]float64,
) ([]ProfitabilityUserItem, error) {
	activeSubs, err := s.listActiveSubscriptions(ctx, query)
	if err != nil {
		return nil, err
	}

	periodRows, err := s.repo.GetUserAggregates(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("get profitability user aggregates: %w", err)
	}
	periodBySub := make(map[int64]ProfitabilityUserAggregateRow, len(periodRows))
	for _, row := range periodRows {
		if query.PlanID > 0 {
			ratio := groupPlanRatios[row.GroupID][query.PlanID]
			row.StandardCostUSD *= ratio
			row.UsageProxyUSD *= ratio
			row.AccountCostUSD *= ratio
		}
		periodBySub[row.SubscriptionID] = row
	}

	endTime := query.EndTime
	window5h, err := s.repo.GetUserWindowAggregates(ctx, filter, endTime.Add(-5*time.Hour))
	if err != nil {
		return nil, err
	}
	window24h, err := s.repo.GetUserWindowAggregates(ctx, filter, endTime.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	window7d, err := s.repo.GetUserWindowAggregates(ctx, filter, endTime.Add(-7*24*time.Hour))
	if err != nil {
		return nil, err
	}
	if query.PlanID > 0 {
		applySubscriptionWindowRatio(window5h, activeSubs, groupPlanRatios, query.PlanID)
		applySubscriptionWindowRatio(window24h, activeSubs, groupPlanRatios, query.PlanID)
		applySubscriptionWindowRatio(window7d, activeSubs, groupPlanRatios, query.PlanID)
	}

	seriesStart := time.Date(endTime.Year(), endTime.Month(), endTime.Day(), 0, 0, 0, 0, endTime.Location()).AddDate(0, 0, -(profitabilitySeriesDays - 1))
	seriesRows, err := s.repo.GetUserCostSeries(ctx, filter, seriesStart, endTime, "day")
	if err != nil {
		return nil, err
	}
	if query.PlanID > 0 {
		for i := range seriesRows {
			if sub := activeSubs[seriesRows[i].SubscriptionID]; sub != nil {
				ratio := groupPlanRatios[sub.GroupID][query.PlanID]
				seriesRows[i].UsageProxyUSD *= ratio
			}
		}
	}
	dailySeriesBySub := make(map[int64]map[string]float64)
	for _, row := range seriesRows {
		key := row.Bucket.Format("2006-01-02")
		if dailySeriesBySub[row.SubscriptionID] == nil {
			dailySeriesBySub[row.SubscriptionID] = make(map[string]float64)
		}
		dailySeriesBySub[row.SubscriptionID][key] = row.UsageProxyUSD
	}

	items := make([]ProfitabilityUserItem, 0, len(activeSubs))
	accountOnlyMode := isAccountOnlyRevenueMode(query)
	for _, sub := range activeSubs {
		if sub == nil || sub.Group == nil {
			continue
		}
		period := periodBySub[sub.ID]
		if query.AccountID > 0 && period.SubscriptionID == 0 {
			// 账号钻取时只展示在该账号上实际发生过 usage 的订阅
			continue
		}

		revenueRMB, revenuePrecision := profitabilityRevenueForSubscription(sub, period.UsageProxyUSD, cfg.FXRateUSDCNY, orderStats, accountOnlyMode)
		profitRMB := revenueRMB - period.AccountCostUSD*cfg.FXRateUSDCNY
		remainingQuota := profitabilityRemainingQuota(sub)
		burn5h := profitabilityWindowBurn(window5h[sub.ID].UsageProxyUSD, 5)
		burn24h := profitabilityWindowBurn(window24h[sub.ID].UsageProxyUSD, 24)
		burn7d := profitabilityWindowBurn(window7d[sub.ID].UsageProxyUSD, 24*7)
		series := expandDailySeries(dailySeriesBySub[sub.ID], seriesStart, profitabilitySeriesDays)
		forecastDaily := exponentialSmoothingForecast(series, cfg.SmoothingAlpha)
		if forecastDaily <= 0 {
			forecastDaily = profitabilityMaxFloat(window24h[sub.ID].UsageProxyUSD, window7d[sub.ID].UsageProxyUSD/7, window5h[sub.ID].UsageProxyUSD*(24.0/5.0))
		}
		forecastHourly := forecastDaily / 24
		volatility := standardDeviation(series)
		runwayHours := profitabilityRunwayHours(remainingQuota, forecastHourly)
		riskAdjusted := profitabilityRiskAdjustedProfit(revenueRMB/cfg.FXRateUSDCNY-period.AccountCostUSD, volatility)
		items = append(items, ProfitabilityUserItem{
			UserID:                 sub.UserID,
			Email:                  profitabilitySubscriptionEmail(sub, period.Email),
			GroupID:                sub.GroupID,
			GroupName:              sub.Group.Name,
			SubscriptionID:         sub.ID,
			SubscriptionType:       sub.Group.SubscriptionType,
			RecognizedRevenueRMB:   revenueRMB,
			RevenuePrecision:       revenuePrecision,
			UsageProxyUSD:          period.UsageProxyUSD,
			AccountCostUSD:         period.AccountCostUSD,
			StandardCostUSD:        period.StandardCostUSD,
			EstimatedProfitRMB:     profitRMB,
			RemainingQuotaUSD:      remainingQuota,
			BurnRate5hUSDPerHour:   burn5h,
			BurnRate24hUSDPerHour:  burn24h,
			BurnRate7dUSDPerHour:   burn7d,
			ForecastBurnUSDPerHour: forecastHourly,
			RunwayHours:            runwayHours,
			VolatilityUSD:          volatility,
			RiskAdjustedProfitUSD:  riskAdjusted,
			RiskLevel:              profitabilityRiskLevel(runwayHours, nil, nil),
			Precision:              ProfitabilityPrecisionMixed,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].EstimatedProfitRMB == items[j].EstimatedProfitRMB {
			return items[i].SubscriptionID < items[j].SubscriptionID
		}
		return items[i].EstimatedProfitRMB > items[j].EstimatedProfitRMB
	})
	return items, nil
}

func (s *ProfitabilityService) buildAccountRiskItems(
	ctx context.Context,
	query ProfitabilityQuery,
	filter ProfitabilityUsageFilter,
	cfg ProfitabilityConfig,
	accounts []*Account,
	groupPlanRatios map[int64]map[int64]float64,
) ([]ProfitabilityAccountRiskItem, ProfitabilitySummary, error) {
	summary := ProfitabilitySummary{}
	if len(accounts) == 0 {
		return nil, summary, nil
	}

	periodRows, err := s.repo.GetAccountAggregates(ctx, filter)
	if err != nil {
		return nil, summary, fmt.Errorf("get profitability account aggregates: %w", err)
	}
	periodByAccount := make(map[int64]ProfitabilityAccountAggregateRow, len(periodRows))
	for _, row := range periodRows {
		if query.PlanID > 0 {
			ratio := profitabilityAccountPlanRatio(accounts, row.AccountID, query.PlanID, groupPlanRatios)
			row.StandardCostUSD *= ratio
			row.UsageProxyUSD *= ratio
			row.AccountCostUSD *= ratio
		}
		periodByAccount[row.AccountID] = row
	}

	endTime := query.EndTime
	window5h, err := s.repo.GetAccountWindowAggregates(ctx, filter, endTime.Add(-5*time.Hour))
	if err != nil {
		return nil, summary, err
	}
	window24h, err := s.repo.GetAccountWindowAggregates(ctx, filter, endTime.Add(-24*time.Hour))
	if err != nil {
		return nil, summary, err
	}
	window7d, err := s.repo.GetAccountWindowAggregates(ctx, filter, endTime.Add(-7*24*time.Hour))
	if err != nil {
		return nil, summary, err
	}
	if query.PlanID > 0 {
		scaleAccountWindowByPlan(window5h, accounts, query.PlanID, groupPlanRatios)
		scaleAccountWindowByPlan(window24h, accounts, query.PlanID, groupPlanRatios)
		scaleAccountWindowByPlan(window7d, accounts, query.PlanID, groupPlanRatios)
	}

	seriesStart := time.Date(endTime.Year(), endTime.Month(), endTime.Day(), 0, 0, 0, 0, endTime.Location()).AddDate(0, 0, -(profitabilitySeriesDays - 1))
	seriesRows, err := s.repo.GetAccountCostSeries(ctx, filter, seriesStart, endTime, "day")
	if err != nil {
		return nil, summary, err
	}
	if query.PlanID > 0 {
		for i := range seriesRows {
			ratio := profitabilityAccountPlanRatio(accounts, seriesRows[i].AccountID, query.PlanID, groupPlanRatios)
			seriesRows[i].AccountCostUSD *= ratio
		}
	}
	dailySeriesByAccount := make(map[int64]map[string]float64)
	for _, row := range seriesRows {
		key := row.Bucket.Format("2006-01-02")
		if dailySeriesByAccount[row.AccountID] == nil {
			dailySeriesByAccount[row.AccountID] = make(map[string]float64)
		}
		dailySeriesByAccount[row.AccountID][key] = row.AccountCostUSD
	}

	items := make([]ProfitabilityAccountRiskItem, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		period := periodByAccount[account.ID]
		cap5h := profitabilityExtraPositive(account.Extra, "profit_capacity_usd_5h")
		cap7d := profitabilityExtraPositive(account.Extra, "profit_capacity_usd_7d")
		valueCNY := profitabilityExtraPositive(account.Extra, "profit_value_cny")
		observed5h := window5h[account.ID]
		observed24h := window24h[account.ID]
		observed7d := window7d[account.ID]
		used5h, remaining5h := profitabilityCapacityProgress(cap5h, observed5h)
		used7d, remaining7d := profitabilityCapacityProgress(cap7d, observed7d)
		burn5h := profitabilityWindowBurn(observed5h, 5)
		burn24h := profitabilityWindowBurn(observed24h, 24)
		burn7d := profitabilityWindowBurn(observed7d, 24*7)
		series := expandDailySeries(dailySeriesByAccount[account.ID], seriesStart, profitabilitySeriesDays)
		forecastDaily := exponentialSmoothingForecast(series, cfg.SmoothingAlpha)
		if forecastDaily <= 0 {
			forecastDaily = profitabilityMaxFloat(observed24h, observed7d/7, observed5h*(24.0/5.0))
		}
		forecastHourly := forecastDaily / 24
		volatility := standardDeviation(series)
		profitUSD := period.UsageProxyUSD - period.AccountCostUSD
		riskAdjusted := profitabilityRiskAdjustedProfit(profitUSD, volatility)
		runway5h := profitabilityRunwayHours(remaining5h, forecastHourly)
		runway7d := profitabilityRunwayHours(remaining7d, forecastHourly)
		riskLevel := profitabilityRiskLevel(minRunway(runway5h, runway7d), used5h, used7d)

		items = append(items, ProfitabilityAccountRiskItem{
			AccountID:              account.ID,
			Name:                   account.Name,
			Platform:               account.Platform,
			Type:                   account.Type,
			GroupIDs:               append([]int64(nil), account.GroupIDs...),
			GroupNames:             profitabilityAccountGroupNames(account.Groups),
			ProfitValueCNY:         valueCNY,
			ProfitCapacityUSD5h:    cap5h,
			ProfitCapacityUSD7d:    cap7d,
			ObservedCost5hUSD:      observed5h,
			ObservedCost24hUSD:     observed24h,
			ObservedCost7dUSD:      observed7d,
			UsedPercent5h:          used5h,
			UsedPercent7d:          used7d,
			Remaining5hUSD:         remaining5h,
			Remaining7dUSD:         remaining7d,
			BurnRate5hUSDPerHour:   burn5h,
			BurnRate24hUSDPerHour:  burn24h,
			BurnRate7dUSDPerHour:   burn7d,
			ForecastBurnUSDPerHour: forecastHourly,
			Runway5hHours:          runway5h,
			Runway7dHours:          runway7d,
			PeriodUsageProxyUSD:    period.UsageProxyUSD,
			PeriodAccountCostUSD:   period.AccountCostUSD,
			PeriodStandardCostUSD:  period.StandardCostUSD,
			VolatilityUSD:          volatility,
			RiskAdjustedProfitUSD:  riskAdjusted,
			RiskLevel:              riskLevel,
			Precision:              ProfitabilityPrecisionMixed,
		})

		if remaining5h != nil {
			summary.Remaining5hUSD += *remaining5h
			summary.ActiveAccountsWithQuota++
		}
		if remaining7d != nil {
			summary.Remaining7dUSD += *remaining7d
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].PeriodAccountCostUSD == items[j].PeriodAccountCostUSD {
			return items[i].AccountID < items[j].AccountID
		}
		return items[i].PeriodAccountCostUSD > items[j].PeriodAccountCostUSD
	})

	if sumRunway := profitabilitySummaryRunway(items); sumRunway != nil {
		summary.EstimatedRunoutHours = sumRunway
		t := query.EndTime.Add(time.Duration(*sumRunway * float64(time.Hour)))
		summary.EstimatedRunoutAt = &t
	}
	return items, summary, nil
}

func (s *ProfitabilityService) buildTrendPoints(
	query ProfitabilityQuery,
	cfg ProfitabilityConfig,
	trendRows []ProfitabilityUsageTrendRow,
	revenueByBucket map[string]float64,
	mode profitabilityRevenueMode,
) []ProfitabilityTrendPoint {
	rowByKey := make(map[string]ProfitabilityUsageTrendRow, len(trendRows))
	for _, row := range trendRows {
		rowByKey[profitabilityBucketKey(row.Bucket, query.Granularity)] = row
	}

	keys := make([]string, 0, len(rowByKey)+len(revenueByBucket))
	seen := make(map[string]struct{}, len(rowByKey)+len(revenueByBucket))
	for key := range rowByKey {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range revenueByBucket {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	points := make([]ProfitabilityTrendPoint, 0, len(keys))
	for _, key := range keys {
		row := rowByKey[key]
		bucket := row.Bucket
		if bucket.IsZero() {
			if parsed, err := parseTime(key); err == nil {
				bucket = parsed
			}
		}
		revenueRMB := revenueByBucket[key]
		if mode == profitabilityRevenueModeEstimatedFromUsage {
			revenueRMB = row.UsageProxyUSD * cfg.FXRateUSDCNY
		}
		profitRMB := revenueRMB - row.AccountCostUSD*cfg.FXRateUSDCNY
		points = append(points, ProfitabilityTrendPoint{
			Bucket:              bucket,
			RevenueRMB:          revenueRMB,
			RevenuePrecision:    precisionForRevenueMode(mode),
			UsageProxyUSD:       row.UsageProxyUSD,
			AccountCostUSD:      row.AccountCostUSD,
			StandardCostUSD:     row.StandardCostUSD,
			ProfitRMB:           profitRMB,
			ProfitMarginPercent: nullableMargin(profitRMB, revenueRMB),
		})
	}
	return points
}

func (s *ProfitabilityService) buildSummary(
	cfg ProfitabilityConfig,
	trends []ProfitabilityTrendPoint,
	accountSummary ProfitabilitySummary,
	mode profitabilityRevenueMode,
) ProfitabilitySummary {
	summary := accountSummary
	for _, point := range trends {
		summary.RevenueRMB += point.RevenueRMB
		summary.UsageProxyUSD += point.UsageProxyUSD
		summary.AccountCostUSD += point.AccountCostUSD
		summary.StandardCostUSD += point.StandardCostUSD
	}
	summary.RevenuePrecision = precisionForRevenueMode(mode)
	summary.ProfitRMB = summary.RevenueRMB - summary.AccountCostUSD*cfg.FXRateUSDCNY
	summary.ProfitUSD = summary.RevenueRMB/cfg.FXRateUSDCNY - summary.AccountCostUSD
	summary.ProfitMarginPercent = nullableMargin(summary.ProfitRMB, summary.RevenueRMB)
	return summary
}

func (s *ProfitabilityService) buildOptimization(
	cfg ProfitabilityConfig,
	plans []ProfitabilityPlanItem,
	accounts []ProfitabilityAccountRiskItem,
	groups []ProfitabilityGroupItem,
) ProfitabilityOptimizationResult {
	groupRemaining7d := make(map[int64]float64)
	groupRemaining5h := make(map[int64]float64)
	for _, account := range accounts {
		for _, groupID := range account.GroupIDs {
			if account.Remaining7dUSD != nil {
				groupRemaining7d[groupID] += *account.Remaining7dUSD
			}
			if account.Remaining5hUSD != nil {
				groupRemaining5h[groupID] += *account.Remaining5hUSD
			}
		}
	}

	candidates := make([]profitabilityOptimizationCandidate, 0)
	groupIndex := make(map[int64]int)
	groupIDs := make([]int64, 0)
	for _, plan := range plans {
		unitCostUSD := profitabilityUnitCostUSD(plan)
		if unitCostUSD <= 0 {
			continue
		}
		unitRevenueRMB := profitabilityUnitRevenueRMB(plan)
		unitMarginUSD := unitRevenueRMB/cfg.FXRateUSDCNY - unitCostUSD
		if unitMarginUSD <= 0 {
			continue
		}
		if _, ok := groupIndex[plan.GroupID]; !ok {
			groupIndex[plan.GroupID] = len(groupIDs)
			groupIDs = append(groupIDs, plan.GroupID)
		}
		candidates = append(candidates, profitabilityOptimizationCandidate{
			Plan:               plan,
			UnitRevenueRMB:     unitRevenueRMB,
			UnitCostUSD:        unitCostUSD,
			UnitMarginUSD:      unitMarginUSD,
			UnitCost5hUSD:      profitabilityPlanFiveHourUnitCost(plan, unitCostUSD),
			GroupConstraintIdx: groupIndex[plan.GroupID],
		})
	}

	if len(candidates) == 0 || len(groupIDs) == 0 {
		return ProfitabilityOptimizationResult{
			Objective:   "maximize incremental profit under group 5h/7d account capacity constraints",
			Bottlenecks: []string{"没有满足正边际收益且具备历史成本数据的订阅计划"},
			Precision:   ProfitabilityPrecisionEstimated,
		}
	}

	constraints := make([]profitabilityConstraintRow, 0, len(groupIDs)*2)
	for _, groupID := range groupIDs {
		if cap7d := groupRemaining7d[groupID]; cap7d > 0 {
			row := make([]float64, len(candidates))
			for i, candidate := range candidates {
				if candidate.Plan.GroupID == groupID {
					row[i] = candidate.UnitCostUSD
				}
			}
			constraints = append(constraints, profitabilityConstraintRow{
				Key:      fmt.Sprintf("group_%d_7d", groupID),
				Label:    fmt.Sprintf("Group %d 7d capacity", groupID),
				Capacity: cap7d,
				Row:      row,
			})
		}
		if cap5h := groupRemaining5h[groupID]; cap5h > 0 {
			row := make([]float64, len(candidates))
			for i, candidate := range candidates {
				if candidate.Plan.GroupID == groupID {
					row[i] = candidate.UnitCost5hUSD
				}
			}
			constraints = append(constraints, profitabilityConstraintRow{
				Key:      fmt.Sprintf("group_%d_5h", groupID),
				Label:    fmt.Sprintf("Group %d 5h capacity", groupID),
				Capacity: cap5h,
				Row:      row,
			})
		}
	}

	if len(constraints) == 0 {
		return ProfitabilityOptimizationResult{
			Objective:   "maximize incremental profit under group 5h/7d account capacity constraints",
			Bottlenecks: []string{"没有可用于优化的账号容量数据"},
			Precision:   ProfitabilityPrecisionEstimated,
		}
	}

	objective := make([]float64, len(candidates))
	matrix := make([][]float64, len(constraints))
	rhs := make([]float64, len(constraints))
	for i, candidate := range candidates {
		objective[i] = candidate.UnitMarginUSD
	}
	for i, constraint := range constraints {
		matrix[i] = constraint.Row
		rhs[i] = constraint.Capacity
	}

	solution, err := solveLinearProgramSimplex(objective, matrix, rhs)
	if err != nil {
		return ProfitabilityOptimizationResult{
			Objective:   "maximize incremental profit under group 5h/7d account capacity constraints",
			Bottlenecks: []string{err.Error()},
			Precision:   ProfitabilityPrecisionEstimated,
		}
	}

	result := ProfitabilityOptimizationResult{
		Objective:                    "maximize incremental profit under group 5h/7d account capacity constraints",
		Plans:                        make([]ProfitabilityOptimizationPlan, 0),
		Bottlenecks:                  make([]string, 0),
		SensitivityScenarios:         make([]ProfitabilitySensitivityScenario, 0, len(groups)),
		Precision:                    ProfitabilityPrecisionEstimated,
	}

	usedByConstraint := make([]float64, len(constraints))
	for i, candidate := range candidates {
		x := solution[i]
		if x <= 1e-6 {
			continue
		}
		entry := ProfitabilityOptimizationPlan{
			PlanID:                         candidate.Plan.PlanID,
			Name:                           candidate.Plan.Name,
			GroupID:                        candidate.Plan.GroupID,
			GroupName:                      candidate.Plan.GroupName,
			RecommendedAdditionalSales:     profitabilityRoundTo(x, 2),
			EstimatedIncrementalRevenueRMB: profitabilityRoundTo(candidate.UnitRevenueRMB*x, 2),
			EstimatedIncrementalCostUSD:    profitabilityRoundTo(candidate.UnitCostUSD*x, 4),
			EstimatedIncrementalProfitRMB:  profitabilityRoundTo(candidate.UnitMarginUSD*x*cfg.FXRateUSDCNY, 2),
		}
		for j, constraint := range constraints {
			used := constraint.Row[i] * x
			usedByConstraint[j] += used
		}
		result.EstimatedIncrementalRevenueRMB += entry.EstimatedIncrementalRevenueRMB
		result.EstimatedIncrementalCostUSD += entry.EstimatedIncrementalCostUSD
		result.EstimatedIncrementalProfitRMB += entry.EstimatedIncrementalProfitRMB
		result.Plans = append(result.Plans, entry)
	}

	for i, constraint := range constraints {
		remaining := constraint.Capacity - usedByConstraint[i]
		if remaining < 1e-6 {
			result.Bottlenecks = append(result.Bottlenecks, fmt.Sprintf("%s 已成为瓶颈", constraint.Label))
		}
	}
	for _, group := range groups {
		result.SensitivityScenarios = append(result.SensitivityScenarios, ProfitabilitySensitivityScenario{
			Key:             fmt.Sprintf("group_%d", group.GroupID),
			Label:           group.GroupName,
			RevenueDeltaRMB: -group.RecognizedRevenueRMB,
			CostDeltaUSD:    -group.AccountCostUSD,
			ProfitDeltaRMB:  -group.ProfitRMB,
		})
	}

	sort.Slice(result.Plans, func(i, j int) bool {
		if result.Plans[i].EstimatedIncrementalProfitRMB == result.Plans[j].EstimatedIncrementalProfitRMB {
			return result.Plans[i].PlanID < result.Plans[j].PlanID
		}
		return result.Plans[i].EstimatedIncrementalProfitRMB > result.Plans[j].EstimatedIncrementalProfitRMB
	})
	return result
}

func (s *ProfitabilityService) buildPricingRecommendations(
	cfg ProfitabilityConfig,
	plans []ProfitabilityPlanItem,
	accounts []ProfitabilityAccountRiskItem,
) []ProfitabilityPricingRecommendation {
	groupRisk := make(map[int64]string)
	for _, account := range accounts {
		for _, groupID := range account.GroupIDs {
			groupRisk[groupID] = profitabilityWorseRisk(groupRisk[groupID], account.RiskLevel)
		}
	}

	recommendations := make([]ProfitabilityPricingRecommendation, 0, len(plans))
	for _, plan := range plans {
		unitCostUSD := profitabilityUnitCostUSD(plan)
		if unitCostUSD <= 0 {
			continue
		}
		riskPremium := profitabilityRiskPremium(groupRisk[plan.GroupID])
		basePrice := unitCostUSD * cfg.FXRateUSDCNY / math.Max(1-cfg.TargetMargin, 0.01)
		recommended := profitabilityRoundTo(basePrice*(1+riskPremium), 1)
		recommendations = append(recommendations, ProfitabilityPricingRecommendation{
			PlanID:               plan.PlanID,
			Name:                 plan.Name,
			GroupID:              plan.GroupID,
			GroupName:            plan.GroupName,
			CurrentPriceRMB:      plan.PriceRMB,
			RecommendedPriceRMB:  recommended,
			TargetMarginPercent:  profitabilityRoundTo(cfg.TargetMargin*100, 2),
			RiskPremiumPercent:   profitabilityRoundTo(riskPremium*100, 2),
			UnitEstimatedCostUSD: profitabilityRoundTo(unitCostUSD, 6),
			EstimatedProfitRMB:   profitabilityRoundTo(recommended-unitCostUSD*cfg.FXRateUSDCNY, 2),
			Reason:               profitabilityPricingReason(groupRisk[plan.GroupID]),
			Precision:            ProfitabilityPrecisionEstimated,
		})
	}
	sort.Slice(recommendations, func(i, j int) bool {
		if recommendations[i].RecommendedPriceRMB == recommendations[j].RecommendedPriceRMB {
			return recommendations[i].PlanID < recommendations[j].PlanID
		}
		return recommendations[i].RecommendedPriceRMB > recommendations[j].RecommendedPriceRMB
	})
	return recommendations
}

func (s *ProfitabilityService) listAccounts(ctx context.Context, query ProfitabilityQuery) ([]*Account, error) {
	groupFilter := int64(0)
	if query.GroupID > 0 {
		groupFilter = query.GroupID
	}
	if query.AccountID > 0 {
		account, err := s.accountRepo.GetByID(ctx, query.AccountID)
		if err != nil {
			return nil, err
		}
		return []*Account{account}, nil
	}

	page := 1
	result := make([]*Account, 0)
	for {
		items, pag, err := s.accountRepo.ListWithFilters(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  200,
			SortBy:    "name",
			SortOrder: pagination.SortOrderAsc,
		}, "", "", "", "", groupFilter, "")
		if err != nil {
			return nil, err
		}
		for i := range items {
			account := items[i]
			result = append(result, &account)
		}
		if pag == nil || page >= pag.Pages || len(items) == 0 {
			break
		}
		page++
	}
	return result, nil
}

func (s *ProfitabilityService) listActiveSubscriptions(ctx context.Context, query ProfitabilityQuery) (map[int64]*UserSubscription, error) {
	page := 1
	result := make(map[int64]*UserSubscription)
	var userID, groupID *int64
	if query.UserID > 0 {
		userID = &query.UserID
	}
	if query.GroupID > 0 {
		groupID = &query.GroupID
	}
	for {
		items, pag, err := s.userSubRepo.List(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  200,
			SortBy:    "expires_at",
			SortOrder: pagination.SortOrderAsc,
		}, userID, groupID, SubscriptionStatusActive, "", "expires_at", "asc")
		if err != nil {
			return nil, err
		}
		for i := range items {
			sub := items[i]
			result[sub.ID] = &sub
		}
		if pag == nil || page >= pag.Pages || len(items) == 0 {
			break
		}
		page++
	}
	return result, nil
}

type profitabilityRevenueMode string

const (
	profitabilityRevenueModeExact              profitabilityRevenueMode = "exact"
	profitabilityRevenueModeEstimatedFromUsage profitabilityRevenueMode = "estimated_from_usage"
)

func resolveProfitabilityRevenueMode(query ProfitabilityQuery) profitabilityRevenueMode {
	if isAccountOnlyRevenueMode(query) {
		return profitabilityRevenueModeEstimatedFromUsage
	}
	return profitabilityRevenueModeExact
}

func isAccountOnlyRevenueMode(query ProfitabilityQuery) bool {
	return query.AccountID > 0 && query.PlanID == 0 && query.GroupID == 0 && query.UserID == 0
}

type profitabilityOrderStats struct {
	RevenueByPlan        map[int64]float64
	RevenueByGroup       map[int64]float64
	RevenueByUser        map[int64]float64
	RevenueByUserGroup   map[int64]map[int64]float64
	RevenueTrendByBucket map[string]float64
	PlanOrderCount       map[int64]int
	PlanActiveUnits      map[int64]int
	PlanSoldDaysByGroup  map[int64]map[int64]float64
	UserRevenueByPlan    map[int64]map[int64]float64
}

func buildProfitabilityOrderStats(orders []*dbent.PaymentOrder, startTime, endTime, now time.Time, granularity string, plansByID map[int64]*dbent.SubscriptionPlan) profitabilityOrderStats {
	stats := profitabilityOrderStats{
		RevenueByPlan:        make(map[int64]float64),
		RevenueByGroup:       make(map[int64]float64),
		RevenueByUser:        make(map[int64]float64),
		RevenueByUserGroup:   make(map[int64]map[int64]float64),
		RevenueTrendByBucket: make(map[string]float64),
		PlanOrderCount:       make(map[int64]int),
		PlanActiveUnits:      make(map[int64]int),
		PlanSoldDaysByGroup:  make(map[int64]map[int64]float64),
		UserRevenueByPlan:    make(map[int64]map[int64]float64),
	}

	for _, order := range orders {
		if order == nil || order.PlanID == nil || order.SubscriptionGroupID == nil || order.PaidAt == nil {
			continue
		}
		planID := *order.PlanID
		groupID := *order.SubscriptionGroupID
		paidAt := order.PaidAt.UTC()
		validityDays := 0
		if order.SubscriptionDays != nil && *order.SubscriptionDays > 0 {
			validityDays = *order.SubscriptionDays
		} else if plan := plansByID[planID]; plan != nil {
			validityDays = plan.ValidityDays
		}
		netRevenue := order.PayAmount - order.RefundAmount
		if netRevenue < 0 {
			netRevenue = 0
		}
		if !paidAt.Before(startTime) && paidAt.Before(endTime) {
			stats.RevenueByPlan[planID] += netRevenue
			stats.RevenueByGroup[groupID] += netRevenue
			stats.RevenueByUser[order.UserID] += netRevenue
			if stats.RevenueByUserGroup[order.UserID] == nil {
				stats.RevenueByUserGroup[order.UserID] = make(map[int64]float64)
			}
			stats.RevenueByUserGroup[order.UserID][groupID] += netRevenue
			key := profitabilityBucketKey(paidAt, granularity)
			stats.RevenueTrendByBucket[key] += netRevenue
			stats.PlanOrderCount[planID]++
			if stats.UserRevenueByPlan[order.UserID] == nil {
				stats.UserRevenueByPlan[order.UserID] = make(map[int64]float64)
			}
			stats.UserRevenueByPlan[order.UserID][planID] += netRevenue
		}

		if validityDays > 0 {
			orderEnd := paidAt.Add(time.Duration(validityDays) * 24 * time.Hour)
			overlapDays := profitabilityOverlapDays(paidAt, orderEnd, startTime, endTime)
			if overlapDays > 0 {
				if stats.PlanSoldDaysByGroup[groupID] == nil {
					stats.PlanSoldDaysByGroup[groupID] = make(map[int64]float64)
				}
				stats.PlanSoldDaysByGroup[groupID][planID] += overlapDays
			}
			if paidAt.Before(now) && orderEnd.After(now) {
				stats.PlanActiveUnits[planID]++
			}
		}
	}
	return stats
}

func buildGroupPlanRatios(input map[int64]map[int64]float64) map[int64]map[int64]float64 {
	out := make(map[int64]map[int64]float64, len(input))
	for groupID, planDays := range input {
		out[groupID] = make(map[int64]float64, len(planDays))
		total := 0.0
		for _, soldDays := range planDays {
			total += soldDays
		}
		for planID, soldDays := range planDays {
			if total > 0 {
				out[groupID][planID] = soldDays / total
			}
		}
	}
	return out
}

func profitabilityBucketKey(t time.Time, granularity string) string {
	if strings.EqualFold(strings.TrimSpace(granularity), "hour") {
		return t.UTC().Format("2006-01-02T15:00:00Z")
	}
	return t.UTC().Format("2006-01-02")
}

func profitabilityRevenueForGroup(groupID int64, proxyUsageUSD, fx float64, orderStats profitabilityOrderStats, mode profitabilityRevenueMode) float64 {
	if mode == profitabilityRevenueModeEstimatedFromUsage {
		return proxyUsageUSD * fx
	}
	return orderStats.RevenueByGroup[groupID]
}

func profitabilityRevenueForPlan(planID int64, proxyUsageUSD, fx float64, orderStats profitabilityOrderStats, mode profitabilityRevenueMode) float64 {
	if mode == profitabilityRevenueModeEstimatedFromUsage {
		return proxyUsageUSD * fx
	}
	return orderStats.RevenueByPlan[planID]
}

func profitabilityRevenueForSubscription(sub *UserSubscription, proxyUsageUSD, fx float64, orderStats profitabilityOrderStats, accountOnlyMode bool) (float64, ProfitabilityPrecision) {
	if sub == nil {
		return 0, ProfitabilityPrecisionEstimated
	}
	if accountOnlyMode {
		return proxyUsageUSD * fx, ProfitabilityPrecisionEstimated
	}
	if byGroup := orderStats.RevenueByUserGroup[sub.UserID]; byGroup != nil {
		return byGroup[sub.GroupID], ProfitabilityPrecisionExact
	}
	return 0, ProfitabilityPrecisionExact
}

func profitabilityRemainingQuota(sub *UserSubscription) *float64 {
	if sub == nil || sub.Group == nil {
		return nil
	}
	if sub.Group.IsTotalQuotaSubscriptionType() {
		value := sub.TotalRemainingUSD
		return &value
	}
	remaining := make([]float64, 0, 3)
	if sub.Group.HasDailyLimit() {
		remaining = append(remaining, profitabilityMaxFloat(0, *sub.Group.DailyLimitUSD-sub.DailyUsageUSD))
	}
	if sub.Group.HasWeeklyLimit() {
		remaining = append(remaining, profitabilityMaxFloat(0, *sub.Group.WeeklyLimitUSD-sub.WeeklyUsageUSD))
	}
	if sub.Group.HasMonthlyLimit() {
		remaining = append(remaining, profitabilityMaxFloat(0, *sub.Group.MonthlyLimitUSD-sub.MonthlyUsageUSD))
	}
	if len(remaining) == 0 {
		return nil
	}
	value := remaining[0]
	for _, item := range remaining[1:] {
		if item < value {
			value = item
		}
	}
	return &value
}

func profitabilityWindowBurn(totalUSD float64, windowHours float64) float64 {
	if windowHours <= 0 {
		return 0
	}
	return totalUSD / windowHours
}

func profitabilityRunwayHours(remaining *float64, hourlyBurn float64) *float64 {
	if remaining == nil || *remaining <= 0 || hourlyBurn <= 0 {
		return nil
	}
	value := *remaining / hourlyBurn
	return &value
}

func profitabilityRiskAdjustedProfit(profitUSD, volatility float64) *float64 {
	if volatility <= 0 {
		return nil
	}
	value := profitUSD / volatility
	return &value
}

func profitabilityRiskLevel(runway *float64, used5h, used7d *float64) string {
	if runway != nil && *runway < 24 {
		return "high"
	}
	if used5h != nil && *used5h >= 85 {
		return "high"
	}
	if used7d != nil && *used7d >= 85 {
		return "high"
	}
	if runway != nil && *runway < 72 {
		return "medium"
	}
	return "low"
}

func minRunway(values ...*float64) *float64 {
	var out *float64
	for _, value := range values {
		if value == nil {
			continue
		}
		if out == nil || *value < *out {
			v := *value
			out = &v
		}
	}
	return out
}

func profitabilityExtraPositive(extra map[string]any, key string) *float64 {
	if len(extra) == 0 {
		return nil
	}
	value := parseExtraFloat64(extra[key])
	if value <= 0 {
		return nil
	}
	return &value
}

func profitabilityCapacityProgress(capacity *float64, observed float64) (*float64, *float64) {
	if capacity == nil || *capacity <= 0 {
		return nil, nil
	}
	used := observed / *capacity * 100
	remaining := profitabilityMaxFloat(0, *capacity-observed)
	return &used, &remaining
}

func profitabilityAccountGroupNames(groups []*Group) []string {
	if len(groups) == 0 {
		return nil
	}
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		if group == nil || strings.TrimSpace(group.Name) == "" {
			continue
		}
		out = append(out, group.Name)
	}
	sort.Strings(out)
	return out
}

func profitabilitySubscriptionEmail(sub *UserSubscription, fallback string) string {
	if sub != nil && sub.User != nil && strings.TrimSpace(sub.User.Email) != "" {
		return sub.User.Email
	}
	return fallback
}

func expandDailySeries(points map[string]float64, start time.Time, days int) []float64 {
	if days <= 0 {
		return nil
	}
	result := make([]float64, 0, days)
	for i := 0; i < days; i++ {
		key := start.AddDate(0, 0, i).Format("2006-01-02")
		result = append(result, points[key])
	}
	return result
}

func exponentialSmoothingForecast(series []float64, alpha float64) float64 {
	if len(series) == 0 {
		return 0
	}
	if alpha <= 0 || alpha > 1 {
		alpha = defaultProfitabilitySmoothingAlpha
	}
	level := series[0]
	for _, value := range series[1:] {
		level = alpha*value + (1-alpha)*level
	}
	return level
}

func standardDeviation(series []float64) float64 {
	if len(series) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range series {
		sum += value
	}
	mean := sum / float64(len(series))
	variance := 0.0
	for _, value := range series {
		diff := value - mean
		variance += diff * diff
	}
	variance /= float64(len(series))
	return math.Sqrt(variance)
}

func nullableMargin(profit, revenue float64) *float64 {
	if revenue <= 0 {
		return nil
	}
	value := (profit / revenue) * 100
	return &value
}

func profitabilitySummaryRunway(items []ProfitabilityAccountRiskItem) *float64 {
	totalRemaining := 0.0
	totalBurn := 0.0
	for _, item := range items {
		if item.Remaining7dUSD != nil {
			totalRemaining += *item.Remaining7dUSD
		}
		totalBurn += item.ForecastBurnUSDPerHour
	}
	if totalRemaining <= 0 || totalBurn <= 0 {
		return nil
	}
	value := totalRemaining / totalBurn
	return &value
}

func profitabilitySummaryRunwayFromGroups(groups []ProfitabilityGroupItem, cfg ProfitabilityConfig) *float64 {
	totalRevenueUSD := 0.0
	totalCostUSD := 0.0
	for _, group := range groups {
		totalRevenueUSD += group.RecognizedRevenueRMB / cfg.FXRateUSDCNY
		totalCostUSD += group.AccountCostUSD
	}
	if totalCostUSD <= 0 {
		return nil
	}
	value := totalRevenueUSD / totalCostUSD
	return &value
}

func scaleAccountWindowByPlan(values map[int64]float64, accounts []*Account, planID int64, ratios map[int64]map[int64]float64) {
	for accountID, value := range values {
		ratio := profitabilityAccountPlanRatio(accounts, accountID, planID, ratios)
		values[accountID] = value * ratio
	}
}

func profitabilityAccountPlanRatio(accounts []*Account, accountID, planID int64, ratios map[int64]map[int64]float64) float64 {
	for _, account := range accounts {
		if account == nil || account.ID != accountID {
			continue
		}
		maxRatio := 0.0
		for _, groupID := range account.GroupIDs {
			if ratio := ratios[groupID][planID]; ratio > maxRatio {
				maxRatio = ratio
			}
		}
		return maxRatio
	}
	return 0
}

func applySubscriptionWindowRatio(values map[int64]ProfitabilitySubscriptionWindowUsageRow, subs map[int64]*UserSubscription, ratios map[int64]map[int64]float64, planID int64) {
	for subID, row := range values {
		if sub := subs[subID]; sub != nil {
			ratio := ratios[sub.GroupID][planID]
			row.UsageProxyUSD *= ratio
			row.AccountCostUSD *= ratio
			values[subID] = row
		}
	}
}

func profitabilityOverlapDays(startA, endA, startB, endB time.Time) float64 {
	start := startA
	if startB.After(start) {
		start = startB
	}
	end := endA
	if endB.Before(end) {
		end = endB
	}
	if !end.After(start) {
		return 0
	}
	return end.Sub(start).Hours() / 24
}

func profitabilityUnitCostUSD(plan ProfitabilityPlanItem) float64 {
	if plan.SoldCount > 0 {
		return plan.EstimatedAccountCostUSD / float64(plan.SoldCount)
	}
	return 0
}

func profitabilityUnitRevenueRMB(plan ProfitabilityPlanItem) float64 {
	if plan.SoldCount > 0 {
		return plan.RecognizedRevenueRMB / float64(plan.SoldCount)
	}
	return plan.PriceRMB
}

func profitabilityPlanFiveHourUnitCost(plan ProfitabilityPlanItem, unitCostUSD float64) float64 {
	if plan.ValidityDays <= 0 {
		return unitCostUSD
	}
	spreadHours := float64(plan.ValidityDays * 24)
	return unitCostUSD * (5 / spreadHours)
}

func profitabilityPricingReason(riskLevel string) string {
	switch riskLevel {
	case "high":
		return "高承压账号占比较高，建议提高风险溢价"
	case "medium":
		return "部分账号 runway 偏短，建议保守提价"
	default:
		return "当前承压平稳，按目标利润率给出建议价"
	}
}

func profitabilityRiskPremium(riskLevel string) float64 {
	switch riskLevel {
	case "high":
		return 0.12
	case "medium":
		return 0.05
	default:
		return 0
	}
}

func profitabilityWorseRisk(current, next string) string {
	order := map[string]int{"low": 1, "medium": 2, "high": 3}
	if order[next] > order[current] {
		return next
	}
	if current == "" {
		return next
	}
	return current
}

func trimProfitabilityPlans(items []ProfitabilityPlanItem, limit int) []ProfitabilityPlanItem {
	if len(items) <= limit || limit <= 0 {
		return items
	}
	return items[:limit]
}

func trimProfitabilityGroups(items []ProfitabilityGroupItem, limit int) []ProfitabilityGroupItem {
	if len(items) <= limit || limit <= 0 {
		return items
	}
	return items[:limit]
}

func trimProfitabilityUsers(items []ProfitabilityUserItem, limit int) []ProfitabilityUserItem {
	if len(items) <= limit || limit <= 0 {
		return items
	}
	return items[:limit]
}

func trimProfitabilityAccounts(items []ProfitabilityAccountRiskItem, limit int) []ProfitabilityAccountRiskItem {
	if len(items) <= limit || limit <= 0 {
		return items
	}
	return items[:limit]
}

func trimProfitabilityPricing(items []ProfitabilityPricingRecommendation, limit int) []ProfitabilityPricingRecommendation {
	if len(items) <= limit || limit <= 0 {
		return items
	}
	return items[:limit]
}

func normalizeProfitabilityQuery(query ProfitabilityQuery) ProfitabilityQuery {
	if query.Granularity != "hour" {
		query.Granularity = "day"
	}
	if query.EndTime.IsZero() {
		query.EndTime = time.Now().UTC()
	}
	if query.StartTime.IsZero() {
		query.StartTime = query.EndTime.AddDate(0, 0, -7)
	}
	if !query.EndTime.After(query.StartTime) {
		query.EndTime = query.StartTime.Add(24 * time.Hour)
	}
	return query
}

func precisionForRevenueMode(mode profitabilityRevenueMode) ProfitabilityPrecision {
	if mode == profitabilityRevenueModeEstimatedFromUsage {
		return ProfitabilityPrecisionEstimated
	}
	return ProfitabilityPrecisionExact
}

func planPrecisionForMode(mode profitabilityRevenueMode) ProfitabilityPrecision {
	if mode == profitabilityRevenueModeEstimatedFromUsage {
		return ProfitabilityPrecisionEstimated
	}
	return ProfitabilityPrecisionMixed
}

func profitabilityMetaBundle(mode profitabilityRevenueMode) ProfitabilityMetaBundle {
	revenuePrecision := precisionForRevenueMode(mode)
	return ProfitabilityMetaBundle{
		Summary: ProfitabilityMeta{Precision: revenuePrecision, Description: "汇总利润卡片中的收入字段在 account 钻取场景会退化为估算值"},
		Trends: ProfitabilityMeta{Precision: revenuePrecision, Description: "收入趋势在 account 钻取场景按 usage proxy * 汇率估算"},
		Plans: ProfitabilityMeta{Precision: planPrecisionForMode(mode), Description: "计划维度成本与利润按订阅天数比例估算"},
		Groups: ProfitabilityMeta{Precision: revenuePrecision, Description: "分组维度成本精确，account 钻取时收入为估算"},
		Users: ProfitabilityMeta{Precision: ProfitabilityPrecisionMixed, Description: "用户 remaining quota 精确，forecast/runway/risk 为算法推导"},
		Accounts: ProfitabilityMeta{Precision: ProfitabilityPrecisionMixed, Description: "账号成本精确，容量与 runway 依赖手工配置及算法推导"},
		Optimization: ProfitabilityMeta{Precision: ProfitabilityPrecisionEstimated, Description: "LP 优化基于历史单位成本和当前账号容量给出建议"},
		Pricing: ProfitabilityMeta{Precision: ProfitabilityPrecisionEstimated, Description: "动态定价基于历史单位成本与风险溢价给出建议"},
	}
}

func profitabilityPrecisionNotes(mode profitabilityRevenueMode) []ProfitabilityPrecisionNote {
	return []ProfitabilityPrecisionNote{
		{Key: "revenue", Label: "确收金额", Precision: precisionForRevenueMode(mode), Description: "除 account-only 钻取外，收入来自 payment_orders 的确收金额；account-only 时使用 usage proxy 折算"},
		{Key: "group_cost", Label: "分组/用户/账号成本", Precision: ProfitabilityPrecisionExact, Description: "成本来自 usage_logs 聚合的 account_cost / standard_cost"},
		{Key: "plan_cost", Label: "计划成本与利润", Precision: planPrecisionForMode(mode), Description: "计划维度按同组订阅有效天数占比分摊"},
		{Key: "forecast", Label: "Burn / Runway / 风险比", Precision: ProfitabilityPrecisionDerived, Description: "基于指数平滑与波动率计算的推导指标"},
	}
}

func profitabilityRoundTo(value float64, precision int) float64 {
	factor := math.Pow(10, float64(precision))
	return math.Round(value*factor) / factor
}

func profitabilityMaxFloat(values ...float64) float64 {
	max := 0.0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
