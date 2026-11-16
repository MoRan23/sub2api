<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading && !stats && !initializing" class="flex items-center justify-center py-12"><LoadingSpinner /></div>
      <div v-else-if="initializing" class="card p-5" role="status">
        <p class="font-medium">{{ t('dashboard.statsInitializing') }}</p>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('dashboard.statsInitializingHint') }}</p>
        <p v-if="!authStore.isSimpleMode" class="mt-3">{{ t('dashboard.balance') }}: ${{ totalBalance.toFixed(8) }}</p>
        <button class="btn btn-secondary mt-3" :disabled="loading" @click="refreshAll">{{ t('common.refresh') }}</button>
      </div>
      <template v-else-if="stats">
        <UserDashboardStats :stats="stats" :balance="totalBalance" :is-simple="authStore.isSimpleMode" :platform-quotas="platformQuotas" />
        <UserDashboardCharts v-model:startDate="startDate" v-model:endDate="endDate" v-model:granularity="granularity" :loading="loadingCharts" :trend="trendData" :models="modelStats" @dateRangeChange="loadCharts" @granularityChange="loadCharts" @refresh="refreshAll" />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div class="lg:col-span-2"><UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" /></div>
          <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'; import { useI18n } from 'vue-i18n'; import { useAuthStore } from '@/stores/auth'; import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import AppLayout from '@/components/layout/AppLayout.vue'; import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'; import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'; import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import type { UsageLog, TrendDataPoint, ModelStat, PlatformQuotaItem } from '@/types'
import { getMyPlatformQuotas } from '@/api/user'
import { formatDateLocalInput } from '@/utils/format'

const authStore = useAuthStore(); const user = computed(() => authStore.user)
const { t } = useI18n()
const initializing = ref(false)
let retryTimer: ReturnType<typeof setTimeout> | undefined
let retryCount = 0
let disposed = false
const totalBalance = computed(() => {
  const value = Number(user.value?.total_balance)
  return Number.isFinite(value)
    ? value
    : Number(user.value?.balance || 0) + Number(user.value?.gift_balance || 0)
})
const stats = ref<UserStatsType | null>(null); const loading = ref(false); const loadingUsage = ref(false); const loadingCharts = ref(false)
const trendData = ref<TrendDataPoint[]>([]); const modelStats = ref<ModelStat[]>([]); const recentUsage = ref<UsageLog[]>([])
const platformQuotas = ref<PlatformQuotaItem[] | null>(null)

const startDate = ref(formatDateLocalInput(new Date(Date.now() - 6 * 86400000))); const endDate = ref(formatDateLocalInput(new Date())); const granularity = ref('day')

const loadStats = async () => {
  if (loading.value || disposed) return
  if (retryTimer) clearTimeout(retryTimer)
  loading.value = true
  try {
    await authStore.refreshUser()
    const value = await usageAPI.getDashboardStats()
    if (disposed) return
    stats.value = value
    initializing.value = false
    retryCount = 0
  } catch (error) {
    if (disposed) return
    const detail = error as { reason?: string; code?: string; retry_after_seconds?: number }
    initializing.value = detail.reason === 'USAGE_STATS_INITIALIZING' || detail.code === 'USAGE_STATS_INITIALIZING'
    if (initializing.value && retryCount++ < 6) {
      const delay = Math.min(60, Math.max(10, detail.retry_after_seconds || 10))
      retryTimer = setTimeout(loadStats, delay * 1000)
    } else if (!initializing.value) console.error('Failed to load dashboard stats:', error)
  } finally { loading.value = false }
}
const loadCharts = async () => { loadingCharts.value = true; try { const res = await Promise.all([usageAPI.getDashboardTrend({ start_date: startDate.value, end_date: endDate.value, granularity: granularity.value as any }), usageAPI.getDashboardModels({ start_date: startDate.value, end_date: endDate.value })]); trendData.value = res[0].trend || []; modelStats.value = res[1].models || [] } catch (error) { console.error('Failed to load charts:', error) } finally { loadingCharts.value = false } }
const loadRecent = async () => { loadingUsage.value = true; try { const res = await usageAPI.getByDateRange(startDate.value, endDate.value); recentUsage.value = res.items.slice(0, 5) } catch (error) { console.error('Failed to load recent usage:', error) } finally { loadingUsage.value = false } }
const loadPlatformQuotas = async () => { try { const data = await getMyPlatformQuotas(); platformQuotas.value = data.platform_quotas ?? [] } catch (error) { console.warn('Failed to load platform quotas:', error); platformQuotas.value = [] } }
const refreshAll = () => { loadStats(); loadCharts(); loadRecent(); loadPlatformQuotas() }

onMounted(() => { refreshAll() })
onBeforeUnmount(() => { disposed = true; if (retryTimer) clearTimeout(retryTimer) })
</script>
