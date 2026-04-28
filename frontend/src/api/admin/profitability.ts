import { apiClient } from '../client'

export type ProfitabilityPrecision = 'exact' | 'estimated' | 'mixed' | 'derived'

export interface ProfitabilityConfig {
  fx_rate_usd_cny: number
  target_margin: number
  smoothing_alpha: number
}

export interface ProfitabilityQueryParams {
  start_date?: string
  end_date?: string
  granularity?: 'day' | 'hour'
  plan_id?: number
  group_id?: number
  user_id?: number
  account_id?: number
}

export interface ProfitabilityMeta {
  precision: ProfitabilityPrecision
  description: string
}

export interface ProfitabilitySummary {
  revenue_rmb: number
  revenue_precision: ProfitabilityPrecision
  usage_proxy_usd: number
  account_cost_usd: number
  standard_cost_usd: number
  profit_rmb: number
  profit_usd: number
  profit_margin_percent?: number | null
  remaining_5h_usd: number
  remaining_7d_usd: number
  estimated_runout_hours?: number | null
  estimated_runout_at?: string | null
  active_accounts_with_quota: number
}

export interface ProfitabilityTrendPoint {
  bucket: string
  revenue_rmb: number
  revenue_precision: ProfitabilityPrecision
  usage_proxy_usd: number
  account_cost_usd: number
  standard_cost_usd: number
  profit_rmb: number
  profit_margin_percent?: number | null
}

export interface ProfitabilityPlanItem {
  plan_id: number
  group_id: number
  group_name: string
  group_platform: string
  name: string
  price_rmb: number
  original_price_rmb?: number | null
  validity_days: number
  sold_count: number
  active_sales_estimate: number
  recognized_revenue_rmb: number
  estimated_usage_proxy_usd: number
  estimated_account_cost_usd: number
  estimated_standard_cost_usd: number
  estimated_profit_rmb: number
  profit_margin_percent?: number | null
  allocation_sold_days: number
  precision: ProfitabilityPrecision
}

export interface ProfitabilityGroupItem {
  group_id: number
  group_name: string
  group_platform: string
  subscription_type: string
  recognized_revenue_rmb: number
  usage_proxy_usd: number
  account_cost_usd: number
  standard_cost_usd: number
  profit_rmb: number
  profit_margin_percent?: number | null
  marginal_contribution_rmb: number
  sensitivity_delta_profit_rmb: number
  precision: ProfitabilityPrecision
}

export interface ProfitabilityUserItem {
  user_id: number
  email: string
  group_id: number
  group_name: string
  subscription_id: number
  subscription_type: string
  recognized_revenue_rmb: number
  revenue_precision: ProfitabilityPrecision
  usage_proxy_usd: number
  account_cost_usd: number
  standard_cost_usd: number
  estimated_profit_rmb: number
  remaining_quota_usd?: number | null
  burn_rate_5h_usd_per_hour: number
  burn_rate_24h_usd_per_hour: number
  burn_rate_7d_usd_per_hour: number
  forecast_burn_usd_per_hour: number
  runway_hours?: number | null
  volatility_usd: number
  risk_adjusted_profit_usd?: number | null
  risk_level: string
  precision: ProfitabilityPrecision
}

export interface ProfitabilityAccountRiskItem {
  account_id: number
  name: string
  platform: string
  type: string
  group_ids?: number[]
  group_names?: string[]
  profit_value_cny?: number | null
  profit_capacity_usd_5h?: number | null
  profit_capacity_usd_7d?: number | null
  observed_cost_5h_usd: number
  observed_cost_24h_usd: number
  observed_cost_7d_usd: number
  used_percent_5h?: number | null
  used_percent_7d?: number | null
  remaining_5h_usd?: number | null
  remaining_7d_usd?: number | null
  burn_rate_5h_usd_per_hour: number
  burn_rate_24h_usd_per_hour: number
  burn_rate_7d_usd_per_hour: number
  forecast_burn_usd_per_hour: number
  runway_5h_hours?: number | null
  runway_7d_hours?: number | null
  period_usage_proxy_usd: number
  period_account_cost_usd: number
  period_standard_cost_usd: number
  volatility_usd: number
  risk_adjusted_profit_usd?: number | null
  risk_level: string
  precision: ProfitabilityPrecision
}

export interface ProfitabilityConstraint {
  key: string
  label: string
  capacity: number
  used: number
  remaining: number
}

export interface ProfitabilityOptimizationPlan {
  plan_id: number
  name: string
  group_id: number
  group_name: string
  recommended_additional_sales: number
  estimated_incremental_revenue_rmb: number
  estimated_incremental_cost_usd: number
  estimated_incremental_profit_rmb: number
  binding_constraints?: ProfitabilityConstraint[]
}

export interface ProfitabilitySensitivityScenario {
  key: string
  label: string
  revenue_delta_rmb: number
  cost_delta_usd: number
  profit_delta_rmb: number
}

export interface ProfitabilityOptimizationResult {
  objective: string
  estimated_incremental_profit_rmb: number
  estimated_incremental_revenue_rmb: number
  estimated_incremental_cost_usd: number
  plans: ProfitabilityOptimizationPlan[]
  bottlenecks: string[]
  sensitivity_scenarios: ProfitabilitySensitivityScenario[]
  precision: ProfitabilityPrecision
}

export interface ProfitabilityPricingRecommendation {
  plan_id: number
  name: string
  group_id: number
  group_name: string
  current_price_rmb: number
  recommended_price_rmb: number
  target_margin_percent: number
  risk_premium_percent: number
  unit_estimated_cost_usd: number
  estimated_profit_rmb: number
  reason: string
  precision: ProfitabilityPrecision
}

export interface ProfitabilityPrecisionNote {
  key: string
  label: string
  precision: ProfitabilityPrecision
  description: string
}

export interface ProfitabilitySnapshot {
  generated_at: string
  start_date: string
  end_date: string
  applied_filters: Record<string, unknown>
  config: ProfitabilityConfig
  meta: {
    summary: ProfitabilityMeta
    trends: ProfitabilityMeta
    plans: ProfitabilityMeta
    groups: ProfitabilityMeta
    users: ProfitabilityMeta
    accounts: ProfitabilityMeta
    optimization: ProfitabilityMeta
    pricing: ProfitabilityMeta
  }
  precision_notes: ProfitabilityPrecisionNote[]
  summary: ProfitabilitySummary
  trends: ProfitabilityTrendPoint[]
  plans: ProfitabilityPlanItem[]
  groups: ProfitabilityGroupItem[]
  users: ProfitabilityUserItem[]
  accounts: ProfitabilityAccountRiskItem[]
  optimization: ProfitabilityOptimizationResult
  pricing_recommendations: ProfitabilityPricingRecommendation[]
}

export interface ProfitabilityAccountValueUpdate {
  account_id: number
  profit_value_cny?: number | null
  profit_capacity_usd_5h?: number | null
  profit_capacity_usd_7d?: number | null
}

async function getSnapshot(params?: ProfitabilityQueryParams): Promise<ProfitabilitySnapshot> {
  const { data } = await apiClient.get<ProfitabilitySnapshot>('/admin/dashboard/profitability/snapshot', { params })
  return data
}

async function getPlans(params?: ProfitabilityQueryParams): Promise<ProfitabilityPlanItem[]> {
  const { data } = await apiClient.get<{ items: ProfitabilityPlanItem[] }>('/admin/dashboard/profitability/plans', { params })
  return data.items
}

async function getGroups(params?: ProfitabilityQueryParams): Promise<ProfitabilityGroupItem[]> {
  const { data } = await apiClient.get<{ items: ProfitabilityGroupItem[] }>('/admin/dashboard/profitability/groups', { params })
  return data.items
}

async function getUsers(params?: ProfitabilityQueryParams): Promise<ProfitabilityUserItem[]> {
  const { data } = await apiClient.get<{ items: ProfitabilityUserItem[] }>('/admin/dashboard/profitability/users', { params })
  return data.items
}

async function getAccountRisk(params?: ProfitabilityQueryParams): Promise<ProfitabilityAccountRiskItem[]> {
  const { data } = await apiClient.get<{ items: ProfitabilityAccountRiskItem[] }>('/admin/dashboard/profitability/accounts/risk', { params })
  return data.items
}

async function getOptimization(params?: ProfitabilityQueryParams): Promise<ProfitabilityOptimizationResult> {
  const { data } = await apiClient.get<ProfitabilityOptimizationResult>('/admin/dashboard/profitability/optimization', { params })
  return data
}

async function getPricingRecommendations(params?: ProfitabilityQueryParams): Promise<ProfitabilityPricingRecommendation[]> {
  const { data } = await apiClient.get<{ items: ProfitabilityPricingRecommendation[] }>('/admin/dashboard/profitability/pricing-recommendations', { params })
  return data.items
}

async function getConfig(): Promise<ProfitabilityConfig> {
  const { data } = await apiClient.get<ProfitabilityConfig>('/admin/dashboard/profitability/config')
  return data
}

async function updateConfig(payload: ProfitabilityConfig): Promise<ProfitabilityConfig> {
  const { data } = await apiClient.put<ProfitabilityConfig>('/admin/dashboard/profitability/config', payload)
  return data
}

async function updateAccountValues(items: ProfitabilityAccountValueUpdate[]): Promise<{ updated: number }> {
  const { data } = await apiClient.post<{ updated: number }>('/admin/dashboard/profitability/account-values/batch', { items })
  return data
}

export const profitabilityAPI = {
  getSnapshot,
  getPlans,
  getGroups,
  getUsers,
  getAccountRisk,
  getOptimization,
  getPricingRecommendations,
  getConfig,
  updateConfig,
  updateAccountValues
}

export default profitabilityAPI
