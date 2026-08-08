<template>
  <section aria-labelledby="prompt-policy-title" class="py-6">
    <div>
      <h2 id="prompt-policy-title" class="text-base font-semibold text-gray-950 dark:text-white">{{ t('admin.promptAudit.policy.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.promptAudit.policy.description') }}</p>
    </div>

    <div class="mt-5 grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(260px,0.45fr)]">
      <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-700/60 dark:bg-dark-900/20 sm:p-5">
        <GroupScopeSelector
          v-model:all-groups="guardAllGroups"
          v-model:group-ids="guardGroupIds"
          :title="t('admin.promptAudit.policy.guardScope')"
          name="prompt-audit-guard-scope"
          :groups
          :search-aria-label="t('admin.promptAudit.policy.searchGuardGroups')"
          data-test="guard-group-scope"
        />

        <fieldset class="mt-5 border-t border-gray-100 pt-5 dark:border-dark-800">
          <legend class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.promptAudit.policy.scanners') }}</legend>
          <div class="mt-3 grid gap-2 sm:grid-cols-2">
            <label v-for="scanner in SCANNER_CATALOG" :key="scanner.id" class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-gray-700 hover:bg-gray-50 dark:text-dark-200 dark:hover:bg-dark-800">
              <input type="checkbox" :checked="draft.scanners.includes(scanner.id)" :aria-label="scannerLabel(scanner.id)" @change="toggleScanner(scanner.id)" />
              <span>{{ scannerLabel(scanner.id) }}</span>
            </label>
          </div>
        </fieldset>

        <fieldset class="mt-5 border-t border-gray-100 pt-5 dark:border-dark-800">
          <legend class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.promptAudit.policy.keywordTitle') }}</legend>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.policy.keywordDescription') }}</p>
          <div class="mt-3">
            <KeywordEditor
              v-model="keywordModel"
              :label="t('admin.promptAudit.policy.keywords')"
              :placeholder="t('admin.promptAudit.policy.keywordsPlaceholder')"
              :count-label="t('admin.promptAudit.policy.keywordCount', { count: effectiveKeywordCount })"
              :limit-label="t('admin.promptAudit.policy.keywordLimit')"
              :error="keywordError"
            />
          </div>
          <GroupScopeSelector
            v-model:all-groups="keywordAllGroups"
            v-model:group-ids="keywordGroupIds"
            class="mt-5 border-t border-gray-100 pt-5 dark:border-dark-800"
            :title="t('admin.promptAudit.policy.keywordScope')"
            name="prompt-audit-keyword-scope"
            :groups
            :search-aria-label="t('admin.promptAudit.policy.searchKeywordGroups')"
            data-test="keyword-group-scope"
          />
        </fieldset>
      </div>

      <div class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700/60 dark:bg-dark-900/20 sm:p-5">
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.promptAudit.policy.workerCount') }}</span>
          <input :value="draft.worker_count" type="number" min="1" max="32" class="input mt-1.5 w-full" :aria-label="t('admin.promptAudit.policy.workerCount')" @input="patch({ worker_count: Number(($event.target as HTMLInputElement).value) })" />
        </label>
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.promptAudit.policy.queueCapacity') }}</span>
          <input :value="draft.queue_capacity" type="number" min="1" max="100000" class="input mt-1.5 w-full" :aria-label="t('admin.promptAudit.policy.queueCapacity')" @input="patch({ queue_capacity: Number(($event.target as HTMLInputElement).value) })" />
        </label>
        <div class="rounded-lg bg-gray-50 px-4 py-3 text-sm text-gray-600 dark:bg-dark-900/50 dark:text-dark-300">
          <p class="font-medium text-gray-800 dark:text-dark-100">{{ t('admin.promptAudit.policy.strategy') }}</p>
          <p class="mt-1">priority · {{ t('admin.promptAudit.policy.strategyHint') }}</p>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PromptAuditDraft, PromptAuditGroup } from '../types'
import { cloneData, SCANNER_CATALOG } from '../viewModel'
import GroupScopeSelector from './GroupScopeSelector.vue'
import KeywordEditor from './KeywordEditor.vue'

const props = defineProps<{ draft: PromptAuditDraft; groups: PromptAuditGroup[] }>()
const emit = defineEmits<{ (event: 'update:draft', value: PromptAuditDraft): void }>()
const { t } = useI18n()
const guardAllGroups = computed({ get: () => props.draft.all_groups, set: (value) => patch({ all_groups: value }) })
const guardGroupIds = computed({ get: () => props.draft.group_ids, set: (value) => patch({ group_ids: value }) })
const keywordAllGroups = computed({ get: () => props.draft.keyword_all_groups, set: (value) => patch({ keyword_all_groups: value }) })
const keywordGroupIds = computed({ get: () => props.draft.keyword_group_ids, set: (value) => patch({ keyword_group_ids: value }) })
const keywordModel = computed<string[]>({
  get: () => props.draft.blocked_keywords,
  set: (value) => patch({ blocked_keywords: value }),
})
const effectiveKeywords = computed(() => {
  const seen = new Set<string>()
  return props.draft.blocked_keywords.map((value) => value.trim()).filter((value) => {
    if (!value) return false
    const key = value.toLowerCase()
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
})
const effectiveKeywordCount = computed(() => effectiveKeywords.value.length)
const keywordError = computed(() => {
  if (effectiveKeywordCount.value > 10000) return t('admin.promptAudit.errors.prompt_audit_too_many_keywords')
  if (effectiveKeywords.value.some((value) => Array.from(value).length > 200)) return t('admin.promptAudit.errors.prompt_audit_keyword_too_long')
  return ''
})

function patch(value: Partial<PromptAuditDraft>) {
  emit('update:draft', { ...cloneData(props.draft), ...value })
}
function toggleScanner(id: string) {
  const selected = new Set(props.draft.scanners)
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  patch({ scanners: SCANNER_CATALOG.map((item) => item.id).filter((item) => selected.has(item)) })
}
function scannerLabel(id: string): string {
  return t(`admin.promptAudit.scanners.${id}`)
}
</script>
