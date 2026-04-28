<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="card p-5">
        <div class="flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
          <div class="space-y-3">
            <div>
              <h1 class="text-xl font-semibold text-gray-900 dark:text-white">利润分析</h1>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                基于确收、用户营收代理值、账号真实成本和账号容量，统一查看订阅收益、用户 burn rate、账号承压与定价建议。
              </p>
            </div>

            <div class="flex flex-wrap items-center gap-3">
              <div class="w-full sm:w-auto">
                <DateRangePicker
                  v-model:start-date="filters.startDate"
                  v-model:end-date="filters.endDate"
                />
              </div>
              <div class="w-28">
                <Select v-model="filters.granularity" :options="granularityOptions" />
              </div>
              <div class="w-52">
                <Select v-model="filters.groupId" :options="groupOptions" placeholder="按分组过滤" />
              </div>
              <div class="w-56">
                <Select v-model="filters.planId" :options="planOptions" placeholder="按计划过滤" />
              </div>
              <input
                v-model.trim="filters.userId"
                type="number"
                min="1"
                class="input w-36"
                placeholder="用户 ID"
              />
              <input
                v-model.trim="filters.accountId"
                type="number"
                min="1"
                class="input w-36"
                placeholder="账号 ID"
              />
            </div>

            <div class="flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span v-if="filters.accountId" class="rounded-full bg-amber-100 px-2 py-1 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">
                当前为账号钻取模式，部分收入会退化为估算值
              </span>
              <span v-if="filters.planId" class="rounded-full bg-blue-100 px-2 py-1 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300">
                计划维度成本按订阅天数占比分摊
              </span>
            </div>
          </div>

          <div class="grid gap-3 sm:grid-cols-3 xl:w-[420px]">
            <div class="space-y-2">
              <label class="input-label">固定汇率 USD/CNY</label>
              <input v-model.number="configDraft.fx_rate_usd_cny" type="number" min="0.01" step="0.01" class="input" />
            </div>
            <div class="space-y-2">
              <label class="input-label">目标利润率</label>
              <input v-model.number="configDraft.target_margin" type="number" min="0" max="0.99" step="0.01" class="input" />
            </div>
            <div class="space-y-2">
              <label class="input-label">平滑系数 α</label>
              <input v-model.number="configDraft.smoothing_alpha" type="number" min="0.01" max="1" step="0.01" class="input" />
            </div>
            <div class="sm:col-span-3 flex flex-wrap justify-end gap-2">
              <button class="btn btn-secondary" :disabled="loading" @click="resetFilters">
                清空过滤
              </button>
              <button class="btn btn-secondary" :disabled="configSaving" @click="saveConfig">
                {{ configSaving ? '保存中…' : '保存参数' }}
              </button>
              <button class="btn btn-primary" :disabled="loading" @click="applyFilters">
                {{ loading ? '加载中…' : '刷新面板' }}
              </button>
            </div>
          </div>
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
            <p class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">总确收 RMB</p>
            <p class="mt-2 text-2xl font-bold text-gray-900 dark:text-white">{{ formatCurrency(snapshot.summary.revenue_rmb, 'CNY') }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              精度: {{ precisionLabel(snapshot.summary.revenue_precision) }}
            </p>
          </div>
          <div class="card p-4">
            <p class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">总使用 / 成本</p>
            <p class="mt-2 text-2xl font-bold text-gray-900 dark:text-white">{{ formatCurrency(snapshot.summary.usage_proxy_usd, 'USD') }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              账号成本 {{ formatCurrency(snapshot.summary.account_cost_usd, 'USD') }}
            </p>
          </div>
          <div class="card p-4">
            <p class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">总利润 / 利润率</p>
            <p class="mt-2 text-2xl font-bold" :class="snapshot.summary.profit_rmb >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'">
              {{ formatCurrency(snapshot.summary.profit_rmb, 'CNY') }}
            </p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ formatPercent(snapshot.summary.profit_margin_percent) }}
            </p>
          </div>
          <div class="card p-4">
            <p class="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-gray-400">5h / 7d 剩余额度</p>
            <p class="mt-2 text-lg font-bold text-gray-900 dark:text-white">
              {{ formatCurrency(snapshot.summary.remaining_5h_usd, 'USD') }}
            </p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              7d {{ formatCurrency(snapshot.summary.remaining_7d_usd, 'USD') }}
            </p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              预计 runout {{ formatRunout(snapshot.summary.estimated_runout_hours) }}
            </p>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-6 xl:grid-cols-2">
          <div class="card p-4">
            <div class="mb-4 flex items-center justify-between">
              <div>
                <h2 class="text-sm font-semibold text-gray-900 dark:text-white">收入 / 成本 / 利润趋势</h2>
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  收入按 {{ precisionLabel(snapshot.meta.trends.precision) }} 口径展示
                </p>
              </div>
              <span class="rounded-full bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-200">
                {{ filters.granularity === 'hour' ? '按小时' : '按天' }}
              </span>
            </div>
            <div class="h-80">
              <Line v-if="trendChartData" :data="trendChartData" :options="trendChartOptions" />
            </div>
          </div>

          <div class="card p-4">
            <div class="mb-4">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">排行 / 承压分布</h2>
              <p class="text-xs text-gray-500 dark:text-gray-400">左图按计划利润排序，右图查看 7d 承压最高账号</p>
            </div>
            <div class="grid grid-cols-1 gap-6 xl:grid-cols-2">
              <div class="h-72">
                <Bar v-if="planProfitChartData" :data="planProfitChartData" :options="barChartOptions" />
              </div>
              <div class="h-72">
                <Bar v-if="accountRiskChartData" :data="accountRiskChartData" :options="barChartOptions" />
              </div>
            </div>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-6 xl:grid-cols-2">
          <div class="card p-4">
            <div class="mb-4">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">精确值 vs 估算值</h2>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ snapshot.meta.summary.description }}</p>
            </div>
            <div class="space-y-3">
              <div
                v-for="note in snapshot.precision_notes"
                :key="note.key"
                class="rounded-xl border border-gray-200 px-4 py-3 dark:border-dark-700"
              >
                <div class="flex items-center justify-between gap-3">
                  <div class="font-medium text-gray-900 dark:text-white">{{ note.label }}</div>
                  <span class="rounded-full px-2 py-1 text-xs" :class="precisionBadgeClass(note.precision)">
                    {{ precisionLabel(note.precision) }}
                  </span>
                </div>
                <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ note.description }}</p>
              </div>
            </div>
          </div>

          <div class="card p-4">
            <div class="mb-4">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">订阅组合优化建议</h2>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                严格 LP 求解，结果仅作建议，不自动回写业务配置
              </p>
            </div>
            <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
              <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-800">
                <div class="text-xs text-gray-500 dark:text-gray-400">增量收入</div>
                <div class="mt-1 font-semibold text-gray-900 dark:text-white">{{ formatCurrency(snapshot.optimization.estimated_incremental_revenue_rmb, 'CNY') }}</div>
              </div>
              <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-800">
                <div class="text-xs text-gray-500 dark:text-gray-400">增量成本</div>
                <div class="mt-1 font-semibold text-gray-900 dark:text-white">{{ formatCurrency(snapshot.optimization.estimated_incremental_cost_usd, 'USD') }}</div>
              </div>
              <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-800">
                <div class="text-xs text-gray-500 dark:text-gray-400">增量利润</div>
                <div class="mt-1 font-semibold text-emerald-600 dark:text-emerald-400">{{ formatCurrency(snapshot.optimization.estimated_incremental_profit_rmb, 'CNY') }}</div>
              </div>
            </div>
            <div class="mt-4 space-y-3">
              <div v-if="snapshot.optimization.bottlenecks.length" class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
                {{ snapshot.optimization.bottlenecks.join('；') }}
              </div>
              <div v-for="item in snapshot.optimization.plans" :key="item.plan_id" class="rounded-xl border border-gray-200 px-4 py-3 dark:border-dark-700">
                <div class="flex items-start justify-between gap-3">
                  <div>
                    <div class="font-medium text-gray-900 dark:text-white">{{ item.name }}</div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ item.group_name || `#${item.group_id}` }}</div>
                  </div>
                  <div class="text-right text-sm">
                    <div class="font-semibold text-gray-900 dark:text-white">+{{ item.recommended_additional_sales.toFixed(2) }} 份</div>
                    <div class="text-emerald-600 dark:text-emerald-400">{{ formatCurrency(item.estimated_incremental_profit_rmb, 'CNY') }}</div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div class="card p-4">
          <div class="mb-4 flex items-center justify-between gap-4">
            <div>
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">计划收益排行与动态定价建议</h2>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                计划维度收入精确，成本按同组有效订阅天数分摊。可直接在这里改当前售价。
              </p>
            </div>
          </div>
          <div class="overflow-x-auto">
            <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800/80">
                <tr>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">计划</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">分组</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">售价</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">已售</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">确收</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">账号成本</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">利润率</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">建议价</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">操作</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                <tr v-for="plan in snapshot.plans" :key="plan.plan_id">
                  <td class="px-3 py-3 align-top">
                    <div class="font-medium text-gray-900 dark:text-white">{{ plan.name }}</div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">ID {{ plan.plan_id }}</div>
                  </td>
                  <td class="px-3 py-3 align-top">
                    <div class="text-gray-900 dark:text-white">{{ plan.group_name || `#${plan.group_id}` }}</div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ plan.group_platform }}</div>
                  </td>
                  <td class="px-3 py-3 align-top">
                    <div class="flex flex-col gap-2">
                      <input v-model.number="planDrafts[plan.plan_id].price" type="number" min="0.01" step="0.01" class="input w-28" />
                      <input v-model.number="planDrafts[plan.plan_id].originalPrice" type="number" min="0" step="0.01" class="input w-28" placeholder="原价" />
                    </div>
                  </td>
                  <td class="px-3 py-3 align-top text-gray-900 dark:text-white">
                    {{ plan.sold_count }}
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">有效 {{ plan.allocation_sold_days.toFixed(1) }} 天</div>
                  </td>
                  <td class="px-3 py-3 align-top">
                    <div class="text-gray-900 dark:text-white">{{ formatCurrency(plan.recognized_revenue_rmb, 'CNY') }}</div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ precisionLabel(plan.precision) }}</div>
                  </td>
                  <td class="px-3 py-3 align-top text-orange-600 dark:text-orange-400">{{ formatCurrency(plan.estimated_account_cost_usd, 'USD') }}</td>
                  <td class="px-3 py-3 align-top">
                    <div :class="plan.estimated_profit_rmb >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'">
                      {{ formatPercent(plan.profit_margin_percent) }}
                    </div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ formatCurrency(plan.estimated_profit_rmb, 'CNY') }}</div>
                  </td>
                  <td class="px-3 py-3 align-top">
                    <div class="font-medium text-gray-900 dark:text-white">
                      {{ formatCurrency(pricingMap.get(plan.plan_id)?.recommended_price_rmb ?? plan.price_rmb, 'CNY') }}
                    </div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {{ pricingMap.get(plan.plan_id)?.reason || '暂无建议' }}
                    </div>
                  </td>
                  <td class="px-3 py-3 align-top">
                    <button class="btn btn-secondary px-3 py-1 text-xs" :disabled="savingPlanId === plan.plan_id" @click="savePlanPrice(plan)">
                      {{ savingPlanId === plan.plan_id ? '保存中…' : '保存价格' }}
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-6 xl:grid-cols-2">
          <div class="card p-4">
            <div class="mb-4">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">分组收益与敏感度</h2>
              <p class="text-xs text-gray-500 dark:text-gray-400">每个分组都视作一条利润贡献路径，移除该分组时的利润变动见敏感度列。</p>
            </div>
            <div class="overflow-x-auto">
              <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
                <thead class="bg-gray-50 dark:bg-dark-800/80">
                  <tr>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">分组</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">确收</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">成本</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">利润</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">敏感度</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                  <tr v-for="group in snapshot.groups" :key="group.group_id">
                    <td class="px-3 py-3">
                      <div class="font-medium text-gray-900 dark:text-white">{{ group.group_name || `#${group.group_id}` }}</div>
                      <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ group.group_platform }}</div>
                    </td>
                    <td class="px-3 py-3">{{ formatCurrency(group.recognized_revenue_rmb, 'CNY') }}</td>
                    <td class="px-3 py-3">{{ formatCurrency(group.account_cost_usd, 'USD') }}</td>
                    <td class="px-3 py-3" :class="group.profit_rmb >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'">
                      {{ formatCurrency(group.profit_rmb, 'CNY') }}
                    </td>
                    <td class="px-3 py-3 text-gray-600 dark:text-gray-300">{{ formatCurrency(group.sensitivity_delta_profit_rmb, 'CNY') }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div class="card p-4">
            <div class="mb-4">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">用户 Burn Rate / Runway</h2>
              <p class="text-xs text-gray-500 dark:text-gray-400">remaining quota 为精确值；forecast、runway、风险收益比为推导值。</p>
            </div>
            <div class="overflow-x-auto">
              <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
                <thead class="bg-gray-50 dark:bg-dark-800/80">
                  <tr>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">用户</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">剩余额度</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">5h / 24h / 7d</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">Forecast</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">Runway</th>
                    <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">风险</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                  <tr v-for="user in snapshot.users" :key="user.subscription_id">
                    <td class="px-3 py-3">
                      <div class="font-medium text-gray-900 dark:text-white">{{ user.email }}</div>
                      <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ user.group_name }}</div>
                    </td>
                    <td class="px-3 py-3">{{ formatNullableCurrency(user.remaining_quota_usd, 'USD') }}</td>
                    <td class="px-3 py-3">
                      <div>{{ formatCurrency(user.burn_rate_5h_usd_per_hour, 'USD') }}/h</div>
                      <div class="text-xs text-gray-500 dark:text-gray-400">
                        24h {{ formatCurrency(user.burn_rate_24h_usd_per_hour, 'USD') }}/h · 7d {{ formatCurrency(user.burn_rate_7d_usd_per_hour, 'USD') }}/h
                      </div>
                    </td>
                    <td class="px-3 py-3">{{ formatCurrency(user.forecast_burn_usd_per_hour, 'USD') }}/h</td>
                    <td class="px-3 py-3">{{ formatRunout(user.runway_hours) }}</td>
                    <td class="px-3 py-3">
                      <span class="rounded-full px-2 py-1 text-xs" :class="riskBadgeClass(user.risk_level)">
                        {{ riskLabel(user.risk_level) }}
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>

        <div class="card p-4">
          <div class="mb-4">
            <h2 class="text-sm font-semibold text-gray-900 dark:text-white">账号承压与价值参数</h2>
            <p class="text-xs text-gray-500 dark:text-gray-400">账号价值参数在这里内联维护，写入 `account.extra`；5h / 7d 剩余额度依赖这些参数。</p>
          </div>
          <div class="overflow-x-auto">
            <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800/80">
                <tr>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">账号</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">业务价值 CNY</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">5h 总值</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">7d 总值</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">观察成本</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">剩余 / Runway</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">风险</th>
                  <th class="px-3 py-2 text-left font-medium text-gray-500 dark:text-gray-300">操作</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                <tr v-for="account in snapshot.accounts" :key="account.account_id">
                  <td class="px-3 py-3">
                    <div class="font-medium text-gray-900 dark:text-white">{{ account.name }}</div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {{ account.group_names?.join(' / ') || account.platform }}
                    </div>
                  </td>
                  <td class="px-3 py-3">
                    <input v-model.number="accountDrafts[account.account_id].profitValueCNY" type="number" min="0" step="0.01" class="input w-28" />
                  </td>
                  <td class="px-3 py-3">
                    <input v-model.number="accountDrafts[account.account_id].profitCapacityUSD5h" type="number" min="0" step="0.01" class="input w-28" />
                  </td>
                  <td class="px-3 py-3">
                    <input v-model.number="accountDrafts[account.account_id].profitCapacityUSD7d" type="number" min="0" step="0.01" class="input w-28" />
                  </td>
                  <td class="px-3 py-3">
                    <div>{{ formatCurrency(account.observed_cost_5h_usd, 'USD') }} / 5h</div>
                    <div class="text-xs text-gray-500 dark:text-gray-400">
                      7d {{ formatCurrency(account.observed_cost_7d_usd, 'USD') }}
                    </div>
                  </td>
                  <td class="px-3 py-3">
                    <div>{{ formatNullableCurrency(account.remaining_5h_usd, 'USD') }} / 5h</div>
                    <div class="text-xs text-gray-500 dark:text-gray-400">
                      7d {{ formatNullableCurrency(account.remaining_7d_usd, 'USD') }} · {{ formatRunout(account.runway_7d_hours) }}
                    </div>
                  </td>
                  <td class="px-3 py-3">
                    <span class="rounded-full px-2 py-1 text-xs" :class="riskBadgeClass(account.risk_level)">
                      {{ riskLabel(account.risk_level) }}
                    </span>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      7d 使用 {{ formatPercent(account.used_percent_7d) }}
                    </div>
                  </td>
                  <td class="px-3 py-3">
                    <button class="btn btn-secondary px-3 py-1 text-xs" :disabled="savingAccountId === account.account_id" @click="saveAccountValues(account.account_id)">
                      {{ savingAccountId === account.account_id ? '保存中…' : '保存参数' }}
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Bar, Line } from 'vue-chartjs'
import {
  BarElement,
  CategoryScale,
  Chart as ChartJS,
  Filler,
  Legend,
  LinearScale,
  LineElement,
  PointElement,
  Title,
  Tooltip
} from 'chart.js'
import AppLayout from '@/components/layout/AppLayout.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Select from '@/components/common/Select.vue'
import type { SelectOption } from '@/components/common/Select.vue'
import { adminAPI } from '@/api/admin'
import type {
  ProfitabilityConfig,
  ProfitabilityPlanItem,
  ProfitabilityPricingRecommendation,
  ProfitabilityQueryParams,
  ProfitabilitySnapshot
} from '@/api/admin/profitability'
import type { SubscriptionPlan } from '@/types/payment'
import type { AdminGroup } from '@/types'
import { useAppStore } from '@/stores'
import { formatCurrency, formatDateOnly, formatDateTime } from '@/utils/format'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, BarElement, Title, Tooltip, Legend, Filler)

const route = useRoute()
const router = useRouter()
const appStore = useAppStore()

const loading = ref(false)
const configSaving = ref(false)
const savingPlanId = ref<number | null>(null)
const savingAccountId = ref<number | null>(null)

const snapshot = ref<ProfitabilitySnapshot | null>(null)
const groups = ref<AdminGroup[]>([])
const plans = ref<SubscriptionPlan[]>([])

const filters = reactive({
  startDate: '',
  endDate: '',
  granularity: 'day' as 'day' | 'hour',
  planId: '',
  groupId: '',
  userId: '',
  accountId: ''
})

const configDraft = reactive<ProfitabilityConfig>({
  fx_rate_usd_cny: 7.2,
  target_margin: 0.25,
  smoothing_alpha: 0.6
})

const accountDrafts = reactive<Record<number, { profitValueCNY: number | null; profitCapacityUSD5h: number | null; profitCapacityUSD7d: number | null }>>({})
const planDrafts = reactive<Record<number, { price: number; originalPrice: number | null }>>({})

const granularityOptions: SelectOption[] = [
  { value: 'day', label: '按天' },
  { value: 'hour', label: '按小时' }
]

const groupOptions = computed<SelectOption[]>(() => [
  { value: '', label: '全部分组' },
  ...groups.value.map((group) => ({ value: String(group.id), label: group.name }))
])

const planOptions = computed<SelectOption[]>(() => [
  { value: '', label: '全部计划' },
  ...plans.value.map((plan) => ({ value: String(plan.id), label: `${plan.name} (#${plan.group_id})` }))
])

const pricingMap = computed(() => {
  const map = new Map<number, ProfitabilityPricingRecommendation>()
  for (const item of snapshot.value?.pricing_recommendations ?? []) {
    map.set(item.plan_id, item)
  }
  return map
})

const trendChartData = computed(() => {
  const points = snapshot.value?.trends ?? []
  if (!points.length) return null
  return {
    labels: points.map((point) => formatTrendBucket(point.bucket)),
    datasets: [
      {
        label: '收入 (RMB)',
        data: points.map((point) => point.revenue_rmb),
        borderColor: '#0f766e',
        backgroundColor: 'rgba(15,118,110,0.15)',
        yAxisID: 'yRmb',
        tension: 0.25
      },
      {
        label: '利润 (RMB)',
        data: points.map((point) => point.profit_rmb),
        borderColor: '#059669',
        backgroundColor: 'rgba(5,150,105,0.1)',
        yAxisID: 'yRmb',
        tension: 0.25
      },
      {
        label: '消耗代理 (USD)',
        data: points.map((point) => point.usage_proxy_usd),
        borderColor: '#2563eb',
        backgroundColor: 'rgba(37,99,235,0.1)',
        yAxisID: 'yUsd',
        tension: 0.25
      },
      {
        label: '账号成本 (USD)',
        data: points.map((point) => point.account_cost_usd),
        borderColor: '#d97706',
        backgroundColor: 'rgba(217,119,6,0.1)',
        yAxisID: 'yUsd',
        tension: 0.25
      }
    ]
  }
})

const trendChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  interaction: { mode: 'index' as const, intersect: false },
  plugins: {
    legend: { position: 'bottom' as const }
  },
  scales: {
    yRmb: {
      type: 'linear' as const,
      position: 'left' as const,
      ticks: {
        callback: (value: string | number) => `${value}`
      }
    },
    yUsd: {
      type: 'linear' as const,
      position: 'right' as const,
      grid: { drawOnChartArea: false },
      ticks: {
        callback: (value: string | number) => `${value}`
      }
    }
  }
}

const planProfitChartData = computed(() => {
  const topPlans = (snapshot.value?.plans ?? []).slice(0, 8)
  if (!topPlans.length) return null
  return {
    labels: topPlans.map((plan) => plan.name),
    datasets: [
      {
        label: '估算利润 (RMB)',
        data: topPlans.map((plan) => plan.estimated_profit_rmb),
        backgroundColor: topPlans.map((plan) => plan.estimated_profit_rmb >= 0 ? 'rgba(5,150,105,0.75)' : 'rgba(220,38,38,0.75)')
      }
    ]
  }
})

const accountRiskChartData = computed(() => {
  const topAccounts = (snapshot.value?.accounts ?? []).slice(0, 8)
  if (!topAccounts.length) return null
  return {
    labels: topAccounts.map((account) => account.name),
    datasets: [
      {
        label: '7d 使用率 (%)',
        data: topAccounts.map((account) => account.used_percent_7d ?? 0),
        backgroundColor: topAccounts.map((account) => riskColor(account.risk_level))
      }
    ]
  }
})

const barChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false }
  }
}

function formatTrendBucket(value: string): string {
  if (!value) return '-'
  return filters.granularity === 'hour'
    ? formatDateTime(value, {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false
    })
    : formatDateOnly(value)
}

function precisionLabel(value: string | null | undefined): string {
  switch (value) {
    case 'exact':
      return '精确'
    case 'estimated':
      return '估算'
    case 'mixed':
      return '混合'
    case 'derived':
      return '推导'
    default:
      return '-'
  }
}

function precisionBadgeClass(value: string | null | undefined): string {
  switch (value) {
    case 'exact':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'estimated':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    case 'mixed':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
    case 'derived':
      return 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300'
    default:
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
  }
}

function riskLabel(value: string | null | undefined): string {
  switch (value) {
    case 'high':
      return '高风险'
    case 'medium':
      return '中风险'
    default:
      return '低风险'
  }
}

function riskBadgeClass(value: string | null | undefined): string {
  switch (value) {
    case 'high':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'medium':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default:
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  }
}

function riskColor(value: string | null | undefined): string {
  switch (value) {
    case 'high':
      return 'rgba(220,38,38,0.75)'
    case 'medium':
      return 'rgba(217,119,6,0.75)'
    default:
      return 'rgba(5,150,105,0.75)'
  }
}

function formatPercent(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '-'
  return `${value.toFixed(1)}%`
}

function formatNullableCurrency(value: number | null | undefined, currency: string): string {
  if (value == null || !Number.isFinite(value)) return '-'
  return formatCurrency(value, currency)
}

function formatRunout(hours: number | null | undefined): string {
  if (hours == null || !Number.isFinite(hours) || hours <= 0) return '-'
  if (hours >= 24) return `${(hours / 24).toFixed(1)} 天`
  return `${hours.toFixed(1)} 小时`
}

function initFiltersFromRoute() {
  const query = route.query
  const today = new Date()
  const endDate = today.toISOString().slice(0, 10)
  const startDate = new Date(today.getTime() - 6 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10)
  filters.startDate = typeof query.start_date === 'string' && query.start_date ? query.start_date : startDate
  filters.endDate = typeof query.end_date === 'string' && query.end_date ? query.end_date : endDate
  filters.granularity = query.granularity === 'hour' ? 'hour' : 'day'
  filters.planId = typeof query.plan_id === 'string' ? query.plan_id : ''
  filters.groupId = typeof query.group_id === 'string' ? query.group_id : ''
  filters.userId = typeof query.user_id === 'string' ? query.user_id : ''
  filters.accountId = typeof query.account_id === 'string' ? query.account_id : ''
}

function buildQueryParams(): ProfitabilityQueryParams {
  const params: ProfitabilityQueryParams = {
    start_date: filters.startDate,
    end_date: filters.endDate,
    granularity: filters.granularity
  }
  const planId = Number(filters.planId)
  const groupId = Number(filters.groupId)
  const userId = Number(filters.userId)
  const accountId = Number(filters.accountId)
  if (Number.isFinite(planId) && planId > 0) params.plan_id = planId
  if (Number.isFinite(groupId) && groupId > 0) params.group_id = groupId
  if (Number.isFinite(userId) && userId > 0) params.user_id = userId
  if (Number.isFinite(accountId) && accountId > 0) params.account_id = accountId
  return params
}

function syncRouteQuery() {
  const params = buildQueryParams()
  router.replace({
    path: '/admin/profitability',
    query: {
      start_date: params.start_date,
      end_date: params.end_date,
      granularity: params.granularity,
      ...(params.plan_id ? { plan_id: String(params.plan_id) } : {}),
      ...(params.group_id ? { group_id: String(params.group_id) } : {}),
      ...(params.user_id ? { user_id: String(params.user_id) } : {}),
      ...(params.account_id ? { account_id: String(params.account_id) } : {})
    }
  })
}

function initDrafts(data: ProfitabilitySnapshot) {
  Object.keys(accountDrafts).forEach((key) => delete accountDrafts[Number(key)])
  Object.keys(planDrafts).forEach((key) => delete planDrafts[Number(key)])

  for (const account of data.accounts) {
    accountDrafts[account.account_id] = {
      profitValueCNY: account.profit_value_cny ?? null,
      profitCapacityUSD5h: account.profit_capacity_usd_5h ?? null,
      profitCapacityUSD7d: account.profit_capacity_usd_7d ?? null
    }
  }

  for (const plan of data.plans) {
    planDrafts[plan.plan_id] = {
      price: plan.price_rmb,
      originalPrice: plan.original_price_rmb ?? null
    }
  }
}

async function loadReferenceData() {
  const [groupList, planResp] = await Promise.all([
    adminAPI.groups.getAll(),
    adminAPI.payment.getPlans()
  ])
  groups.value = groupList
  plans.value = planResp.data
}

async function loadSnapshot() {
  loading.value = true
  try {
    const data = await adminAPI.profitability.getSnapshot(buildQueryParams())
    snapshot.value = data
    configDraft.fx_rate_usd_cny = data.config.fx_rate_usd_cny
    configDraft.target_margin = data.config.target_margin
    configDraft.smoothing_alpha = data.config.smoothing_alpha
    initDrafts(data)
  } catch (error: any) {
    console.error('Failed to load profitability snapshot:', error)
    appStore.showError(error?.message || '加载利润分析面板失败')
  } finally {
    loading.value = false
  }
}

async function applyFilters() {
  syncRouteQuery()
  await loadSnapshot()
}

function resetFilters() {
  const today = new Date()
  filters.endDate = today.toISOString().slice(0, 10)
  filters.startDate = new Date(today.getTime() - 6 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10)
  filters.granularity = 'day'
  filters.planId = ''
  filters.groupId = ''
  filters.userId = ''
  filters.accountId = ''
  void applyFilters()
}

async function saveConfig() {
  configSaving.value = true
  try {
    const saved = await adminAPI.profitability.updateConfig({
      fx_rate_usd_cny: Number(configDraft.fx_rate_usd_cny),
      target_margin: Number(configDraft.target_margin),
      smoothing_alpha: Number(configDraft.smoothing_alpha)
    })
    configDraft.fx_rate_usd_cny = saved.fx_rate_usd_cny
    configDraft.target_margin = saved.target_margin
    configDraft.smoothing_alpha = saved.smoothing_alpha
    appStore.showSuccess('利润分析参数已保存')
    await loadSnapshot()
  } catch (error: any) {
    console.error('Failed to save profitability config:', error)
    appStore.showError(error?.message || '保存利润参数失败')
  } finally {
    configSaving.value = false
  }
}

async function saveAccountValues(accountId: number) {
  const draft = accountDrafts[accountId]
  if (!draft) return
  savingAccountId.value = accountId
  try {
    await adminAPI.profitability.updateAccountValues([
      {
        account_id: accountId,
        profit_value_cny: draft.profitValueCNY,
        profit_capacity_usd_5h: draft.profitCapacityUSD5h,
        profit_capacity_usd_7d: draft.profitCapacityUSD7d
      }
    ])
    appStore.showSuccess(`账号 #${accountId} 的利润参数已保存`)
    await loadSnapshot()
  } catch (error: any) {
    console.error('Failed to save profitability account values:', error)
    appStore.showError(error?.message || '保存账号利润参数失败')
  } finally {
    savingAccountId.value = null
  }
}

async function savePlanPrice(plan: ProfitabilityPlanItem) {
  const draft = planDrafts[plan.plan_id]
  if (!draft) return
  savingPlanId.value = plan.plan_id
  try {
    await adminAPI.payment.updatePlan(plan.plan_id, {
      price: draft.price,
      ...(draft.originalPrice != null ? { original_price: draft.originalPrice } : {})
    })
    appStore.showSuccess(`计划 ${plan.name} 的价格已保存`)
    await loadSnapshot()
  } catch (error: any) {
    console.error('Failed to save profitability plan price:', error)
    appStore.showError(error?.message || '保存计划价格失败')
  } finally {
    savingPlanId.value = null
  }
}

watch(
  () => route.fullPath,
  () => {
    initFiltersFromRoute()
    if (snapshot.value) {
      void loadSnapshot()
    }
  }
)

onMounted(async () => {
  initFiltersFromRoute()
  try {
    await loadReferenceData()
  } catch (error) {
    console.error('Failed to load profitability reference data:', error)
  }
  await loadSnapshot()
})
</script>
