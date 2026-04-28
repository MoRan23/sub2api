package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"golang.org/x/sync/errgroup"
)

const (
	profitabilitySevenDayTotalKey = "profitability_seven_day_total_usd"
	profitabilityAccountValueKey  = "profitability_account_value_usd"

	profitabilityAccountPageSize = 500
	profitabilityWorkerLimit     = 8
)

type ProfitabilityDashboardService struct {
	dashboardService     *DashboardService
	accountRepo          AccountRepository
	usageRepo            UsageLogRepository
	paymentConfigService *PaymentConfigService
}

func NewProfitabilityDashboardService(
	dashboardService *DashboardService,
	accountRepo AccountRepository,
	usageRepo UsageLogRepository,
	paymentConfigService *PaymentConfigService,
) *ProfitabilityDashboardService {
	return &ProfitabilityDashboardService{
		dashboardService:     dashboardService,
		accountRepo:          accountRepo,
		usageRepo:            usageRepo,
		paymentConfigService: paymentConfigService,
	}
}

type ProfitabilityDashboardSnapshot struct {
	StartDate string                               `json:"start_date"`
	EndDate   string                               `json:"end_date"`
	Generated string                               `json:"generated_at"`
	Summary   ProfitabilitySummary                 `json:"summary"`
	Accounts  []ProfitabilityAccountItem           `json:"accounts"`
	Plans     []ProfitabilityPlanItem              `json:"plans"`
	Formula   ProfitabilityFormula                 `json:"formula"`
}

type ProfitabilitySummary struct {
	TotalAccounts          int     `json:"total_accounts"`
	AccountsWith7DayQuota  int     `json:"accounts_with_7day_quota"`
	AccountsWith5HourQuota int     `json:"accounts_with_5hour_quota"`
	TotalRevenueUSD        float64 `json:"total_revenue_usd"`
	TotalAccountCostUSD    float64 `json:"total_account_cost_usd"`
	TotalProfitUSD         float64 `json:"total_profit_usd"`
	ProfitMarginPercent    float64 `json:"profit_margin_percent"`
	TotalRemaining7DayUSD  float64 `json:"total_remaining_7day_usd"`
	TotalRemaining5HourUSD float64 `json:"total_remaining_5hour_usd"`
	EstimatedRunwayDays    float64 `json:"estimated_runway_days"`
	EstimatedRunwayHours   float64 `json:"estimated_runway_hours"`
}

type ProfitabilityFormula struct {
	Runway string `json:"runway"`
	Margin string `json:"margin"`
	ROI    string `json:"roi"`
}

type ProfitabilityAccountItem struct {
	AccountID                 int64    `json:"account_id"`
	Name                      string   `json:"name"`
	Platform                  string   `json:"platform"`
	Type                      string   `json:"type"`
	GroupNames                []string `json:"group_names"`
	RateMultiplier            float64  `json:"rate_multiplier"`
	Configured7DayTotalUSD    *float64 `json:"configured_7day_total_usd,omitempty"`
	ConfiguredAccountValueUSD *float64 `json:"configured_account_value_usd,omitempty"`
	Derived5HourTotalUSD      *float64 `json:"derived_5hour_total_usd,omitempty"`
	Derived7DayTotalUSD       *float64 `json:"derived_7day_total_usd,omitempty"`
	Used5HourPercent          *float64 `json:"used_5hour_percent,omitempty"`
	Used7DayPercent           *float64 `json:"used_7day_percent,omitempty"`
	Used5HourUSD              *float64 `json:"used_5hour_usd,omitempty"`
	Used7DayUSD               *float64 `json:"used_7day_usd,omitempty"`
	Remaining5HourUSD         *float64 `json:"remaining_5hour_usd,omitempty"`
	Remaining7DayUSD          *float64 `json:"remaining_7day_usd,omitempty"`
	FiveHourResetAt           *string  `json:"five_hour_reset_at,omitempty"`
	SevenDayResetAt           *string  `json:"seven_day_reset_at,omitempty"`
	TodayCostUSD              float64  `json:"today_cost_usd"`
	PeriodRevenueUSD          float64  `json:"period_revenue_usd"`
	PeriodAccountCostUSD      float64  `json:"period_account_cost_usd"`
	PeriodProfitUSD           float64  `json:"period_profit_usd"`
	ProfitMarginPercent       float64  `json:"profit_margin_percent"`
	RevenueROIPercent         *float64 `json:"revenue_roi_percent,omitempty"`
	ProjectedDailyCostUSD     float64  `json:"projected_daily_cost_usd"`
	ProjectedHourlyCostUSD    float64  `json:"projected_hourly_cost_usd"`
	EstimatedRunwayDays       *float64 `json:"estimated_runway_days,omitempty"`
	EstimatedRunwayHours      *float64 `json:"estimated_runway_hours,omitempty"`
	Algorithm                 string   `json:"algorithm"`
}

type ProfitabilityPlanItem struct {
	PlanID                int64    `json:"plan_id"`
	GroupID               int64    `json:"group_id"`
	GroupName             string   `json:"group_name"`
	GroupPlatform         string   `json:"group_platform"`
	Name                  string   `json:"name"`
	Price                 float64  `json:"price"`
	OriginalPrice         *float64 `json:"original_price,omitempty"`
	ValidityDays          int      `json:"validity_days"`
	ForSale               bool     `json:"for_sale"`
	SortOrder             int      `json:"sort_order"`
	DailyLimitUSD         *float64 `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD        *float64 `json:"weekly_limit_usd,omitempty"`
	MonthlyLimitUSD       *float64 `json:"monthly_limit_usd,omitempty"`
	SupportedModelScopes  []string `json:"supported_model_scopes,omitempty"`
	CompletedOrders       int      `json:"completed_orders"`
	ActiveSubscriptions   int      `json:"active_subscriptions"`
	RecognizedRevenueUSD  float64  `json:"recognized_revenue_usd"`
	UsageRevenueProxyUSD  float64  `json:"usage_revenue_proxy_usd"`
	UsageAccountCostUSD   float64  `json:"usage_account_cost_usd"`
	EstimatedProfitUSD    float64  `json:"estimated_profit_usd"`
	ProfitMarginPercent   float64  `json:"profit_margin_percent"`
}

func (s *ProfitabilityDashboardService) GetSnapshot(ctx context.Context, startTime, endTime time.Time) (*ProfitabilityDashboardSnapshot, error) {
	accounts, err := s.listAllAccounts(ctx)
	if err != nil {
		return nil, err
	}

	plans, err := s.paymentConfigService.ListPlans(ctx)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}

	now := time.Now().UTC()
	accountItems, summary, err := s.buildAccountItems(ctx, accounts, startTime, endTime, now)
	if err != nil {
		return nil, err
	}

	planItems, err := s.buildPlanItems(ctx, plans, startTime, endTime, now)
	if err != nil {
		return nil, err
	}

	return &ProfitabilityDashboardSnapshot{
		StartDate: startTime.Format("2006-01-02"),
		EndDate:   endTime.Add(-24 * time.Hour).Format("2006-01-02"),
		Generated: now.Format(time.RFC3339),
		Summary:   summary,
		Accounts:  accountItems,
		Plans:     planItems,
		Formula: ProfitabilityFormula{
			Runway: "hourly_burn=max(today_cost/elapsed_hours, cost_5h/5, cost_7d/168, period_cost/range_hours)",
			Margin: "profit_margin=(revenue-account_cost)/revenue",
			ROI:    "revenue_roi=revenue/account_value",
		},
	}, nil
}

func (s *ProfitabilityDashboardService) listAllAccounts(ctx context.Context) ([]Account, error) {
	results := make([]Account, 0, profitabilityAccountPageSize)
	page := 1
	for {
		items, pag, err := s.accountRepo.List(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  profitabilityAccountPageSize,
			SortBy:    "name",
			SortOrder: pagination.SortOrderAsc,
		})
		if err != nil {
			return nil, fmt.Errorf("list accounts: %w", err)
		}
		results = append(results, items...)
		if pag == nil || page >= pag.Pages || len(items) == 0 {
			break
		}
		page++
	}
	return results, nil
}

func (s *ProfitabilityDashboardService) buildAccountItems(
	ctx context.Context,
	accounts []Account,
	startTime, endTime, now time.Time,
) ([]ProfitabilityAccountItem, ProfitabilitySummary, error) {
	summary := ProfitabilitySummary{TotalAccounts: len(accounts)}
	if len(accounts) == 0 {
		return nil, summary, nil
	}

	accountIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		accountIDs = append(accountIDs, account.ID)
	}

	stats5hByAccount, _ := s.getWindowStatsBatch(ctx, accountIDs, now.Add(-5*time.Hour))
	stats7dByAccount, _ := s.getWindowStatsBatch(ctx, accountIDs, now.Add(-7*24*time.Hour))
	todayByAccount, _ := s.getTodayStatsBatch(ctx, accountIDs)

	aggregateByAccount := make(map[int64]*usagestats.UsageStats, len(accounts))
	var aggregateMu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(profitabilityWorkerLimit)
	for i := range accounts {
		account := accounts[i]
		g.Go(func() error {
			stats, err := s.usageRepo.GetAccountStatsAggregated(gctx, account.ID, startTime, endTime)
			if err != nil {
				return fmt.Errorf("aggregate account %d: %w", account.ID, err)
			}
			aggregateMu.Lock()
			aggregateByAccount[account.ID] = stats
			aggregateMu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, summary, err
	}

	rangeHours := math.Max(endTime.Sub(startTime).Hours(), 1)
	rangeDays := math.Max(rangeHours/24, 1)
	items := make([]ProfitabilityAccountItem, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		stats5h := stats5hByAccount[account.ID]
		stats7d := stats7dByAccount[account.ID]
		todayStats := todayByAccount[account.ID]
		agg := aggregateByAccount[account.ID]

		item := ProfitabilityAccountItem{
			AccountID:              account.ID,
			Name:                   account.Name,
			Platform:               account.Platform,
			Type:                   account.Type,
			GroupNames:             accountGroupNames(account.Groups),
			RateMultiplier:         account.BillingRateMultiplier(),
			TodayCostUSD:           statCost(todayStats),
			Algorithm:              "hourly_burn=max(today_cost/elapsed_hours, cost_5h/5, cost_7d/168, period_cost/range_hours)",
			ProjectedDailyCostUSD:  0,
			ProjectedHourlyCostUSD: 0,
		}

		if agg != nil {
			item.PeriodRevenueUSD = agg.TotalActualCost
			if agg.TotalAccountCost != nil {
				item.PeriodAccountCostUSD = *agg.TotalAccountCost
			}
			item.PeriodProfitUSD = item.PeriodRevenueUSD - item.PeriodAccountCostUSD
			item.ProfitMarginPercent = percentage(item.PeriodProfitUSD, item.PeriodRevenueUSD)
			summary.TotalRevenueUSD += item.PeriodRevenueUSD
			summary.TotalAccountCostUSD += item.PeriodAccountCostUSD
		}

		if configured := accountConfiguredFloat64(account.Extra, profitabilitySevenDayTotalKey); configured != nil && *configured > 0 {
			item.Configured7DayTotalUSD = configured
		}
		if configured := accountConfiguredFloat64(account.Extra, profitabilityAccountValueKey); configured != nil && *configured > 0 {
			item.ConfiguredAccountValueUSD = configured
			roi := percentage(item.PeriodRevenueUSD, *configured)
			item.RevenueROIPercent = &roi
		}

		fiveHourProgress := buildProfitabilityFiveHourProgress(account, stats5h, now)
		sevenDayProgress := buildProfitabilitySevenDayProgress(account, stats7d, now)

		if fiveHourProgress != nil {
			assignProgressToAccountItem(&item, fiveHourProgress, true)
			if item.Remaining5HourUSD != nil {
				summary.TotalRemaining5HourUSD += *item.Remaining5HourUSD
				summary.AccountsWith5HourQuota++
			}
		}
		if sevenDayProgress != nil {
			assignProgressToAccountItem(&item, sevenDayProgress, false)
			if item.Remaining7DayUSD != nil {
				summary.TotalRemaining7DayUSD += *item.Remaining7DayUSD
				summary.AccountsWith7DayQuota++
			}
		}

		todayProjectedDaily := projectTodayDailyCost(todayStats, now)
		sevenDayHourly := statCost(stats7d) / math.Max(7*24, 1)
		fiveHourHourly := statCost(stats5h) / 5
		periodHourly := item.PeriodAccountCostUSD / rangeHours
		projectedHourly := maxFloat64(todayProjectedDaily/24, maxFloat64(sevenDayHourly, maxFloat64(fiveHourHourly, periodHourly)))
		projectedDaily := projectedHourly * 24
		item.ProjectedHourlyCostUSD = projectedHourly
		item.ProjectedDailyCostUSD = projectedDaily

		if item.Remaining7DayUSD != nil && projectedDaily > 0 {
			runwayDays := *item.Remaining7DayUSD / projectedDaily
			item.EstimatedRunwayDays = &runwayDays
		}
		if item.Remaining5HourUSD != nil && projectedHourly > 0 {
			runwayHours := *item.Remaining5HourUSD / projectedHourly
			item.EstimatedRunwayHours = &runwayHours
		}

		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].PeriodRevenueUSD == items[j].PeriodRevenueUSD {
			return items[i].Name < items[j].Name
		}
		return items[i].PeriodRevenueUSD > items[j].PeriodRevenueUSD
	})

	summary.TotalProfitUSD = summary.TotalRevenueUSD - summary.TotalAccountCostUSD
	summary.ProfitMarginPercent = percentage(summary.TotalProfitUSD, summary.TotalRevenueUSD)
	totalDailyBurn := 0.0
	totalHourlyBurn := 0.0
	for _, item := range items {
		totalDailyBurn += item.ProjectedDailyCostUSD
		totalHourlyBurn += item.ProjectedHourlyCostUSD
	}
	if totalDailyBurn > 0 {
		summary.EstimatedRunwayDays = summary.TotalRemaining7DayUSD / totalDailyBurn
	}
	if totalHourlyBurn > 0 {
		summary.EstimatedRunwayHours = summary.TotalRemaining5HourUSD / totalHourlyBurn
	}

	return items, summary, nil
}

func (s *ProfitabilityDashboardService) buildPlanItems(
	ctx context.Context,
	plans []*dbent.SubscriptionPlan,
	startTime, endTime, now time.Time,
) ([]ProfitabilityPlanItem, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	groupInfo := s.paymentConfigService.GetGroupInfoMap(ctx, plans)
	planIDs := make([]int64, 0, len(plans))
	groupIDs := make([]int64, 0, len(plans))
	groupSeen := make(map[int64]struct{}, len(plans))
	for _, plan := range plans {
		planIDs = append(planIDs, plan.ID)
		if _, ok := groupSeen[plan.GroupID]; !ok {
			groupSeen[plan.GroupID] = struct{}{}
			groupIDs = append(groupIDs, plan.GroupID)
		}
	}

	revenueByPlan, completedOrdersByPlan, err := s.queryRecognizedRevenueByPlan(ctx, planIDs, startTime, endTime)
	if err != nil {
		return nil, err
	}
	activeSubsByGroup, err := s.queryActiveSubscriptionsByGroup(ctx, groupIDs, now)
	if err != nil {
		return nil, err
	}
	groupUsageByID := make(map[int64]usagestats.GroupStat)
	groupStats, err := s.dashboardService.GetGroupStatsWithFilters(ctx, startTime, endTime, 0, 0, 0, 0, nil, nil, nil)
	if err == nil {
		for _, stat := range groupStats {
			groupUsageByID[stat.GroupID] = stat
		}
	}

	items := make([]ProfitabilityPlanItem, 0, len(plans))
	for _, plan := range plans {
		groupStat := groupUsageByID[plan.GroupID]
		groupMeta := groupInfo[plan.GroupID]
		revenue := revenueByPlan[plan.ID]
		profit := revenue - groupStat.AccountCost
		item := ProfitabilityPlanItem{
			PlanID:               plan.ID,
			GroupID:              plan.GroupID,
			GroupName:            groupMeta.Name,
			GroupPlatform:        groupMeta.Platform,
			Name:                 plan.Name,
			Price:                plan.Price,
			OriginalPrice:        plan.OriginalPrice,
			ValidityDays:         plan.ValidityDays,
			ForSale:              plan.ForSale,
			SortOrder:            plan.SortOrder,
			DailyLimitUSD:        groupMeta.DailyLimitUSD,
			WeeklyLimitUSD:       groupMeta.WeeklyLimitUSD,
			MonthlyLimitUSD:      groupMeta.MonthlyLimitUSD,
			SupportedModelScopes: groupMeta.ModelScopes,
			CompletedOrders:      completedOrdersByPlan[plan.ID],
			ActiveSubscriptions:  activeSubsByGroup[plan.GroupID],
			RecognizedRevenueUSD: revenue,
			UsageRevenueProxyUSD: groupStat.ActualCost,
			UsageAccountCostUSD:  groupStat.AccountCost,
			EstimatedProfitUSD:   profit,
			ProfitMarginPercent:  percentage(profit, revenue),
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].SortOrder == items[j].SortOrder {
			return items[i].PlanID < items[j].PlanID
		}
		return items[i].SortOrder < items[j].SortOrder
	})
	return items, nil
}

func (s *ProfitabilityDashboardService) queryRecognizedRevenueByPlan(
	ctx context.Context,
	planIDs []int64,
	startTime, endTime time.Time,
) (map[int64]float64, map[int64]int, error) {
	revenueByPlan := make(map[int64]float64, len(planIDs))
	completedByPlan := make(map[int64]int, len(planIDs))
	if len(planIDs) == 0 {
		return revenueByPlan, completedByPlan, nil
	}

	orders, err := s.paymentConfigService.entClient.PaymentOrder.Query().
		Where(
			paymentorder.PlanIDIn(planIDs...),
			paymentorder.OrderTypeEQ(payment.OrderTypeSubscription),
			paymentorder.PaidAtNotNil(),
			paymentorder.PaidAtGTE(startTime),
			paymentorder.PaidAtLT(endTime),
		).
		All(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("query subscription orders: %w", err)
	}

	for _, order := range orders {
		netRevenue := order.Amount - order.RefundAmount
		if netRevenue < 0 {
			netRevenue = 0
		}
		revenueByPlan[order.PlanID] += netRevenue
		completedByPlan[order.PlanID]++
	}

	return revenueByPlan, completedByPlan, nil
}

func (s *ProfitabilityDashboardService) queryActiveSubscriptionsByGroup(
	ctx context.Context,
	groupIDs []int64,
	now time.Time,
) (map[int64]int, error) {
	counts := make(map[int64]int, len(groupIDs))
	if len(groupIDs) == 0 {
		return counts, nil
	}

	subs, err := s.paymentConfigService.entClient.UserSubscription.Query().
		Where(
			usersubscription.GroupIDIn(groupIDs...),
			usersubscription.StatusEQ(SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(now),
			usersubscription.DeletedAtIsNil(),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query active subscriptions: %w", err)
	}

	for _, sub := range subs {
		counts[sub.GroupID]++
	}
	return counts, nil
}

func (s *ProfitabilityDashboardService) getWindowStatsBatch(
	ctx context.Context,
	accountIDs []int64,
	startTime time.Time,
) (map[int64]*usagestats.AccountStats, error) {
	if len(accountIDs) == 0 {
		return map[int64]*usagestats.AccountStats{}, nil
	}
	if batchReader, ok := s.usageRepo.(accountWindowStatsBatchReader); ok {
		stats, err := batchReader.GetAccountWindowStatsBatch(ctx, accountIDs, startTime)
		if err == nil {
			return stats, nil
		}
	}

	out := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(profitabilityWorkerLimit)
	for _, accountID := range accountIDs {
		accountID := accountID
		g.Go(func() error {
			stats, err := s.usageRepo.GetAccountWindowStats(gctx, accountID, startTime)
			if err != nil {
				return err
			}
			out[accountID] = stats
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ProfitabilityDashboardService) getTodayStatsBatch(
	ctx context.Context,
	accountIDs []int64,
) (map[int64]*usagestats.AccountStats, error) {
	startOfDay := time.Now().In(time.Local)
	startOfDay = time.Date(startOfDay.Year(), startOfDay.Month(), startOfDay.Day(), 0, 0, 0, 0, startOfDay.Location())
	return s.getWindowStatsBatch(ctx, accountIDs, startOfDay)
}

func buildProfitabilityFiveHourProgress(account *Account, stats *usagestats.AccountStats, now time.Time) *UsageProgress {
	if account == nil {
		return nil
	}

	if account.IsAnthropicOAuthOrSetupToken() {
		progress := &UsageProgress{}
		if account.SessionWindowEnd != nil {
			resetAt := *account.SessionWindowEnd
			progress.ResetsAt = &resetAt
			progress.RemainingSeconds = int(time.Until(resetAt).Seconds())
			if progress.RemainingSeconds < 0 {
				progress.RemainingSeconds = 0
			}
		}
		if stored, ok := account.Extra["session_window_utilization"]; ok {
			progress.Utilization = parseExtraFloat64(stored) * 100
		} else {
			switch account.SessionWindowStatus {
			case "rejected":
				progress.Utilization = 100
			case "allowed_warning":
				progress.Utilization = 80
			}
		}
		progress.WindowStats = windowStatsFromAccountStats(stats)
		return progress
	}

	if account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth {
		progress := buildCodexUsageProgressFromExtra(account.Extra, "5h", now)
		if progress == nil && stats == nil {
			return nil
		}
		if progress == nil {
			progress = &UsageProgress{}
		}
		progress.WindowStats = windowStatsFromAccountStats(stats)
		return progress
	}

	return nil
}

func buildProfitabilitySevenDayProgress(account *Account, stats *usagestats.AccountStats, now time.Time) *UsageProgress {
	if account == nil {
		return nil
	}

	if account.IsAnthropicOAuthOrSetupToken() {
		util := parseExtraFloat64(account.Extra["passive_usage_7d_utilization"])
		resetRaw := parseExtraFloat64(account.Extra["passive_usage_7d_reset"])
		if util <= 0 && resetRaw <= 0 && stats == nil {
			return nil
		}
		progress := &UsageProgress{Utilization: util * 100}
		if resetRaw > 0 {
			resetAt := time.Unix(int64(resetRaw), 0)
			progress.ResetsAt = &resetAt
			progress.RemainingSeconds = int(time.Until(resetAt).Seconds())
			if progress.RemainingSeconds < 0 {
				progress.RemainingSeconds = 0
			}
		}
		progress.WindowStats = windowStatsFromAccountStats(stats)
		return progress
	}

	if account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth {
		progress := buildCodexUsageProgressFromExtra(account.Extra, "7d", now)
		if progress == nil && stats == nil {
			return nil
		}
		if progress == nil {
			progress = &UsageProgress{}
		}
		progress.WindowStats = windowStatsFromAccountStats(stats)
		return progress
	}

	return nil
}

func assignProgressToAccountItem(item *ProfitabilityAccountItem, progress *UsageProgress, fiveHour bool) {
	if item == nil || progress == nil {
		return
	}

	total := deriveWindowTotalUSD(progress, item.Configured7DayTotalUSD, fiveHour)
	usedPercent := progress.Utilization
	usedValue := 0.0
	remainingValue := 0.0
	if total != nil {
		usedValue = *total * clampProgress(usedPercent) / 100
		remainingValue = *total - usedValue
		if remainingValue < 0 {
			remainingValue = 0
		}
	}
	resetAt := optionalRFC3339(progress.ResetsAt)

	if fiveHour {
		item.Derived5HourTotalUSD = total
		item.Used5HourPercent = optionalFloat64(usedPercent)
		if total != nil {
			item.Used5HourUSD = &usedValue
			item.Remaining5HourUSD = &remainingValue
		}
		item.FiveHourResetAt = resetAt
		return
	}

	item.Derived7DayTotalUSD = total
	item.Used7DayPercent = optionalFloat64(usedPercent)
	if total != nil {
		item.Used7DayUSD = &usedValue
		item.Remaining7DayUSD = &remainingValue
	}
	item.SevenDayResetAt = resetAt
}

func deriveWindowTotalUSD(progress *UsageProgress, configured7d *float64, fiveHour bool) *float64 {
	if progress == nil {
		return nil
	}

	if !fiveHour && configured7d != nil && *configured7d > 0 {
		total := *configured7d
		return &total
	}

	if progress.WindowStats != nil {
		if fiveHour && progress.WindowStats.StandardCost > 0 && progress.Utilization > 0 {
			total := progress.WindowStats.StandardCost / (clampProgress(progress.Utilization) / 100)
			if total > 0 {
				return &total
			}
		}
		if !fiveHour && progress.WindowStats.StandardCost > 0 && progress.Utilization > 0 {
			total := progress.WindowStats.StandardCost / (clampProgress(progress.Utilization) / 100)
			if total > 0 {
				return &total
			}
		}
	}

	return nil
}

func accountConfiguredFloat64(extra map[string]any, key string) *float64 {
	if len(extra) == 0 {
		return nil
	}
	value, ok := extra[key]
	if !ok {
		return nil
	}
	parsed := parseExtraFloat64(value)
	return &parsed
}

func accountGroupNames(groups []*Group) []string {
	if len(groups) == 0 {
		return nil
	}
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		if group == nil {
			continue
		}
		names = append(names, group.Name)
	}
	sort.Strings(names)
	return names
}

func projectTodayDailyCost(stats *usagestats.AccountStats, now time.Time) float64 {
	if stats == nil || stats.Cost <= 0 {
		return 0
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	elapsedHours := now.Sub(start).Hours()
	if elapsedHours < 1 {
		elapsedHours = 1
	}
	return (stats.Cost / elapsedHours) * 24
}

func statCost(stats *usagestats.AccountStats) float64 {
	if stats == nil {
		return 0
	}
	return stats.Cost
}

func percentage(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return (numerator / denominator) * 100
}

func clampProgress(progress float64) float64 {
	if progress < 0 {
		return 0
	}
	return progress
}

func maxFloat64(values ...float64) float64 {
	max := 0.0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func optionalFloat64(value float64) *float64 {
	v := value
	return &v
}

func optionalRFC3339(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}
