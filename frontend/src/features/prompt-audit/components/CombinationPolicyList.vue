<template>
  <section class="mt-5 border-t border-gray-100 pt-5 dark:border-dark-800" data-test="combination-policies">
    <h3 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.promptAudit.combination.title') }}</h3>
    <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.combination.description') }}</p>
    <p class="mt-2 text-xs text-amber-700 dark:text-amber-300">{{ t('admin.promptAudit.combination.warning') }}</p>
    <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.policy.keywordRegexHint') }}</p>
    <p class="mt-2 text-xs text-gray-500 dark:text-dark-400" data-test="combination-path-filter-hint">{{ t('admin.promptAudit.combination.pathFilterHint') }}</p>
    <p class="mt-2 text-xs text-gray-500 dark:text-dark-400" data-test="combination-government-template-hint">{{ t('admin.promptAudit.combination.governmentTemplateHint') }}</p>
    <div class="mt-4 flex flex-wrap gap-2">
      <button type="button" class="btn btn-secondary btn-sm" data-test="combination-add-custom" @click="addCustom">{{ t('admin.promptAudit.combination.addCustom') }}</button>
      <button v-for="template in templates" :key="template.id" type="button" class="btn btn-secondary btn-sm" :data-template-id="template.id" data-test="combination-add-template" :disabled="hasTemplate(template.id)" @click="addTemplate(template)">
        {{ t(hasTemplate(template.id) ? 'admin.promptAudit.combination.templateAdded' : 'admin.promptAudit.combination.addTemplate', { name: template.name }) }}
      </button>
    </div>
    <div class="mt-4 space-y-4">
      <CombinationRuleEditor v-for="(policy, index) in modelValue" :key="policy.id" :policy :can-move-up="index > 0" :can-move-down="index < modelValue.length - 1" @update:policy="update(index, $event)" @remove="remove(index)" @move-up="move(index, -1)" @move-down="move(index, 1)" />
      <p v-if="modelValue.length === 0" class="rounded-lg bg-gray-50 p-4 text-sm text-gray-500 dark:bg-dark-900 dark:text-dark-400">{{ t('admin.promptAudit.combination.empty') }}</p>
    </div>
    <p class="mt-3 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.combination.limit') }}</p>
    <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.combination.whitelistLimit', { count: totalWhitelistKeys }) }}</p>
    <p v-if="totalRules > 10000" class="mt-2 text-xs text-red-600 dark:text-red-300" role="alert">{{ t('admin.promptAudit.errors.prompt_audit_too_many_combination_rules') }}</p>
    <p v-if="totalWhitelistKeys > 10000" class="mt-2 text-xs text-red-600 dark:text-red-300" role="alert" data-test="combination-whitelist-limit">{{ t('admin.promptAudit.errors.prompt_audit_too_many_combination_whitelist_keys') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PromptCombinationPolicy } from '../types'
import { cloneData } from '../viewModel'
import { normalizeKeywordRules } from '../keywordRules'
import CombinationRuleEditor from './CombinationRuleEditor.vue'

const props = defineProps<{ modelValue: PromptCombinationPolicy[]; templates: PromptCombinationPolicy[] }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: PromptCombinationPolicy[]): void }>()
const { t } = useI18n()
const totalRules = computed(() => props.modelValue.reduce((total, policy) => total + normalizeKeywordRules(policy.topic_rules ?? []).length + normalizeKeywordRules(policy.intent_rules ?? []).length, 0))
const totalWhitelistKeys = computed(() => props.modelValue.reduce((total, policy) => total + (policy.keyword_whitelist_keys?.length ?? 0), 0))
function hasTemplate(id: string) { return props.modelValue.some((policy) => policy.id === id) }
function addCustom() {
  const uniqueId = globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
  emit('update:modelValue', [...props.modelValue, { id: `rule-${uniqueId}`, name: t('admin.promptAudit.combination.customName', { count: props.modelValue.length + 1 }), enabled: false, topic_rules: [], intent_rules: [], migrate_keywords: [], keyword_whitelist_keys: [] }])
}
function addTemplate(template: PromptCombinationPolicy) {
  if (hasTemplate(template.id)) return
  emit('update:modelValue', [...props.modelValue, { ...cloneData(template), enabled: false, keyword_whitelist_keys: cloneData(template.keyword_whitelist_keys ?? []) }])
}
function update(index: number, value: PromptCombinationPolicy) { emit('update:modelValue', props.modelValue.map((policy, i) => i === index ? value : policy)) }
function remove(index: number) { emit('update:modelValue', props.modelValue.filter((_, i) => i !== index)) }
function move(index: number, direction: number) {
  const target = index + direction
  if (target < 0 || target >= props.modelValue.length) return
  const next = [...props.modelValue]
  ;[next[index], next[target]] = [next[target]!, next[index]!]
  emit('update:modelValue', next)
}
</script>
