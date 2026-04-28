<template>
  <div class="space-y-6">
    <div class="card p-4">
      <div class="flex items-center justify-between gap-3">
        <div>
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">管理员额度/利润面板</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            汇总账号 7day / 5h 剩余额度、账号产出、计划售价和订阅利润。
          </p>
        </div>
        <button class="btn btn-secondary" :disabled="loading" @click="loadSnapshot">
          {{ loading ? '刷新中…' : '刷新面板' }}
        </button>
      </div>
    </div>

    <div v-if="loading && !snapshot" class="card p-8">
      <div class="flex items-center justify-center">
        <LoadingSpinner />
      </div>
    </div>

    <template v-else-if="snapshot">
      <div class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        <div class="card p-4">
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">总 7day 剩余额度</p>
          <p class="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            ${{ formatMoney(snapshot.summary.total_remaining_7day_usd) }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ snapshot.summary.accounts_with_7day_quota }} 个账号参与计算
          </p>
        </div>
        <div class="card p-4">
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">总 5h 剩余额度</p>
          <p class="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            ${{ formatMoney(snapshot.summary.total_remaining_5hour_usd) }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ snapshot.summary.accounts_with_5hour_quota }} 个账号参与计算
          </p>
        </div>
        <div class="card p-4">
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">区间利润</p>
          <p
            class="mt-2 text-2xl font-bold"
            :class="snapshot.summary.total_profit_usd >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'"
          >
            ${{ formatMoney(snapshot.summary.total_profit_usd) }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            毛利率 {{ formatPercent(snapshot.summary.profit_margin_percent) }}
          </p>
        </div>
        <div class="card p-4">
          <p class="text-xs font-medium text-gray-500 dark:text-gray-400">综合续航估算</p>
          <p class="mt-2 text-2xl font-bold text-gray-900 dark:text-white">
            {{ formatRunway(snapshot.summary.estimated_runway_days, 'day') }}
          </p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            5h 侧约 {{ formatRunway(snapshot.summary.estimated_runway_hours, 'hour') }}
          </p>
        </div>
      </div>

      <div class="card p-4">
        <div class="mb-3 flex items-center justify-between gap-3">
          <div>
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">账号利润与额度</h4>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              这里可以维护每个账号的 7day 总值和账号价值，保存后立即重新计算剩余额度与收益率。
            </p>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">
            算法：{{ snapshot.formula.runway }}
          </p>
        </div>
        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-gray-700">
            <thead class="bg-gray-50 dark:bg-gray-800/60">
              <tr>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">账号</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">分组</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">7day 总值</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">账号价值</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">7day 剩余</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">5h 剩余</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">区间营收</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">区间成本</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">利润率</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">续航</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-gray-800">
              <tr v-for="account in snapshot.accounts" :key="account.account_id">
                <td class="px-3 py-3 align-top">
                  <div class="font-medium text-gray-900 dark:text-white">{{ account.name }}</div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ account.platform }} / {{ account.type }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <div class="max-w-[180px] text-xs text-gray-600 dark:text-gray-300">
                    {{ account.group_names?.length ? account.group_names.join(' / ') : '-' }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <input
                    v-model="accountDrafts[account.account_id].quota"
                    type="number"
                    step="0.01"
                    min="0"
                    class="input w-28"
                    placeholder="0"
                  />
                  <p class="mt-1 text-[11px] text-gray-500 dark:text-gray-400">
                    推导 {{ formatNullableMoney(account.derived_7day_total_usd) }}
                  </p>
                </td>
                <td class="px-3 py-3 align-top">
                  <input
                    v-model="accountDrafts[account.account_id].value"
                    type="number"
                    step="0.01"
                    min="0"
                    class="input w-28"
                    placeholder="0"
                  />
                  <p class="mt-1 text-[11px] text-gray-500 dark:text-gray-400">
                    ROI {{ formatNullablePercent(account.revenue_roi_percent) }}
                  </p>
                </td>
                <td class="px-3 py-3 align-top">
                  <div class="font-medium text-gray-900 dark:text-white">
                    {{ formatNullableMoney(account.remaining_7day_usd) }}
                  </div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    已用 {{ formatNullablePercent(account.used_7day_percent) }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <div class="font-medium text-gray-900 dark:text-white">
                    {{ formatNullableMoney(account.remaining_5hour_usd) }}
                  </div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    已用 {{ formatNullablePercent(account.used_5hour_percent) }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top text-emerald-600 dark:text-emerald-400">
                  ${{ formatMoney(account.period_revenue_usd) }}
                </td>
                <td class="px-3 py-3 align-top text-orange-600 dark:text-orange-400">
                  ${{ formatMoney(account.period_account_cost_usd) }}
                </td>
                <td class="px-3 py-3 align-top">
                  <div :class="account.period_profit_usd >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'">
                    ${{ formatMoney(account.period_profit_usd) }}
                  </div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ formatPercent(account.profit_margin_percent) }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <div class="text-gray-900 dark:text-white">
                    7d {{ formatNullableRunway(account.estimated_runway_days, 'day') }}
                  </div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    5h {{ formatNullableRunway(account.estimated_runway_hours, 'hour') }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <button
                    class="btn btn-secondary px-3 py-1 text-xs"
                    :disabled="savingAccountId === account.account_id"
                    @click="saveAccountConfig(account)"
                  >
                    {{ savingAccountId === account.account_id ? '保存中…' : '保存' }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card p-4">
        <div class="mb-3">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">订阅价格与计划利润</h4>
          <p class="text-xs text-gray-500 dark:text-gray-400">
            直接复用当前支付计划配置，更新价格后会重新按时间区间计算计划收入与估算利润。
          </p>
        </div>
        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-gray-700">
            <thead class="bg-gray-50 dark:bg-gray-800/60">
              <tr>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">计划</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">分组</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">售价</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">有效期</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">已成交</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">活动订阅</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">确认收入</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">使用营收代理</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">账号成本</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">估算利润率</th>
                <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">操作</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-gray-800">
              <tr v-for="plan in snapshot.plans" :key="plan.plan_id">
                <td class="px-3 py-3 align-top">
                  <div class="font-medium text-gray-900 dark:text-white">{{ plan.name }}</div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    ID {{ plan.plan_id }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <div class="text-gray-900 dark:text-white">{{ plan.group_name || `#${plan.group_id}` }}</div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ plan.group_platform }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <div class="flex flex-col gap-2">
                    <input
                      v-model="planDrafts[plan.plan_id].price"
                      type="number"
                      step="0.01"
                      min="0.01"
                      class="input w-28"
                    />
                    <input
                      v-model="planDrafts[plan.plan_id].originalPrice"
                      type="number"
                      step="0.01"
                      min="0"
                      class="input w-28"
                      placeholder="原价"
                    />
                  </div>
                </td>
                <td class="px-3 py-3 align-top text-gray-900 dark:text-white">
                  {{ plan.validity_days }} 天
                </td>
                <td class="px-3 py-3 align-top text-gray-900 dark:text-white">
                  {{ plan.completed_orders }}
                </td>
                <td class="px-3 py-3 align-top text-gray-900 dark:text-white">
                  {{ plan.active_subscriptions }}
                </td>
                <td class="px-3 py-3 align-top text-emerald-600 dark:text-emerald-400">
                  ${{ formatMoney(plan.recognized_revenue_usd) }}
                </td>
                <td class="px-3 py-3 align-top text-blue-600 dark:text-blue-400">
                  ${{ formatMoney(plan.usage_revenue_proxy_usd) }}
                </td>
                <td class="px-3 py-3 align-top text-orange-600 dark:text-orange-400">
                  ${{ formatMoney(plan.usage_account_cost_usd) }}
                </td>
                <td class="px-3 py-3 align-top">
                  <div :class="plan.estimated_profit_usd >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'">
                    ${{ formatMoney(plan.estimated_profit_usd) }}
                  </div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ formatPercent(plan.profit_margin_percent) }}
                  </div>
                </td>
                <td class="px-3 py-3 align-top">
                  <button
                    class="btn btn-secondary px-3 py-1 text-xs"
                    :disabled="savingPlanId === plan.plan_id"
                    @click="savePlanConfig(plan)"
                  >
                    {{ savingPlanId === plan.plan_id ? '保存中…' : '保存' }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import type {
  ProfitabilityAccountItem,
  ProfitabilityPlanItem,
  ProfitabilitySnapshotResponse
} from '@/api/admin/dashboard'
import { adminAPI } from '@/api/admin'
import { adminPaymentAPI } from '@/api/admin/payment'
import { useAppStore } from '@/stores/app'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'

const props = defineProps<{
  startDate: string
  endDate: string
}>()

const appStore = useAppStore()
const loading = ref(false)
const savingAccountId = ref<number | null>(null)
const savingPlanId = ref<number | null>(null)
const snapshot = ref<ProfitabilitySnapshotResponse | null>(null)

const accountDrafts = reactive<Record<number, { quota: string; value: string }>>({})
const planDrafts = reactive<Record<number, { price: string; originalPrice: string }>>({})

function initDrafts(data: ProfitabilitySnapshotResponse) {
  data.accounts.forEach((account) => {
    accountDrafts[account.account_id] = {
      quota: account.configured_7day_total_usd ? String(account.configured_7day_total_usd) : '',
      value: account.configured_account_value_usd ? String(account.configured_account_value_usd) : ''
    }
  })

  data.plans.forEach((plan) => {
    planDrafts[plan.plan_id] = {
      price: String(plan.price),
      originalPrice: plan.original_price ? String(plan.original_price) : ''
    }
  })
}

async function loadSnapshot() {
  loading.value = true
  try {
    const response = await adminAPI.dashboard.getProfitabilitySnapshot({
      start_date: props.startDate,
      end_date: props.endDate
    })
    snapshot.value = response
    initDrafts(response)
  } catch (error) {
    console.error('Failed to load profitability snapshot:', error)
    appStore.showError('加载管理员利润面板失败')
  } finally {
    loading.value = false
  }
}

async function saveAccountConfig(account: ProfitabilityAccountItem) {
  const draft = accountDrafts[account.account_id]
  if (!draft) return

  const quota = toPositiveNumberOrZero(draft.quota)
  const value = toPositiveNumberOrZero(draft.value)

  savingAccountId.value = account.account_id
  try {
    await adminAPI.accounts.bulkUpdate([account.account_id], {
      extra: {
        profitability_seven_day_total_usd: quota,
        profitability_account_value_usd: value
      }
    })
    appStore.showSuccess(`已保存账号 ${account.name} 的利润配置`)
    await loadSnapshot()
  } catch (error) {
    console.error('Failed to save account profitability config:', error)
    appStore.showError('保存账号利润配置失败')
  } finally {
    savingAccountId.value = null
  }
}

async function savePlanConfig(plan: ProfitabilityPlanItem) {
  const draft = planDrafts[plan.plan_id]
  if (!draft) return

  const price = Math.max(toPositiveNumberOrZero(draft.price), 0.01)
  const originalPrice = toPositiveNumberOrZero(draft.originalPrice)

  savingPlanId.value = plan.plan_id
  try {
    await adminPaymentAPI.updatePlan(plan.plan_id, {
      price,
      original_price: originalPrice
    })
    appStore.showSuccess(`已保存计划 ${plan.name} 的价格`)
    await loadSnapshot()
  } catch (error) {
    console.error('Failed to save plan profitability config:', error)
    appStore.showError('保存订阅价格失败')
  } finally {
    savingPlanId.value = null
  }
}

function toPositiveNumberOrZero(value: string): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed < 0) {
    return 0
  }
  return parsed
}

function formatMoney(value: number | null | undefined): string {
  const normalized = Number(value ?? 0)
  return normalized.toFixed(2)
}

function formatNullableMoney(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '-'
  }
  return `$${formatMoney(value)}`
}

function formatPercent(value: number | null | undefined): string {
  const normalized = Number(value ?? 0)
  return `${normalized.toFixed(1)}%`
}

function formatNullablePercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return '-'
  }
  return formatPercent(value)
}

function formatRunway(value: number | null | undefined, unit: 'day' | 'hour'): string {
  const normalized = Number(value ?? 0)
  if (!Number.isFinite(normalized) || normalized <= 0) {
    return '-'
  }
  return unit === 'day' ? `${normalized.toFixed(1)} 天` : `${normalized.toFixed(1)} 小时`
}

function formatNullableRunway(value: number | null | undefined, unit: 'day' | 'hour'): string {
  return formatRunway(value, unit)
}

watch(
  () => [props.startDate, props.endDate],
  () => {
    void loadSnapshot()
  },
  { immediate: true }
)
</script>
