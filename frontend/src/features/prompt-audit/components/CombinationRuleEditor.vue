<template>
  <fieldset class="rounded-xl border border-gray-200 p-4 dark:border-dark-700" :data-policy-id="policy.id" data-test="combination-policy">
    <legend class="px-1 text-sm font-medium text-gray-900 dark:text-white">{{ policy.name || t('admin.promptAudit.combination.unnamed') }}</legend>
    <div class="flex flex-wrap items-center justify-between gap-3">
      <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-200">
        <input type="checkbox" :checked="policy.enabled" data-test="combination-enabled" @change="patch({ enabled: ($event.target as HTMLInputElement).checked })" />
        {{ t('admin.promptAudit.combination.enabled') }}
      </label>
      <div class="flex items-center gap-2">
        <button type="button" class="btn btn-secondary btn-sm" data-test="combination-up" :disabled="!canMoveUp" :aria-label="t('admin.promptAudit.combination.moveUp')" @click="$emit('move-up')">↑</button>
        <button type="button" class="btn btn-secondary btn-sm" data-test="combination-down" :disabled="!canMoveDown" :aria-label="t('admin.promptAudit.combination.moveDown')" @click="$emit('move-down')">↓</button>
        <button type="button" class="btn btn-secondary btn-sm text-red-600 dark:text-red-300" data-test="combination-remove" @click="$emit('remove')">{{ t('common.remove') }}</button>
      </div>
    </div>
    <label class="mt-3 block text-sm text-gray-700 dark:text-dark-200">
      <span>{{ t('admin.promptAudit.combination.name') }}</span>
      <input :value="policy.name" class="input mt-1.5 w-full" data-test="combination-name" @input="patch({ name: ($event.target as HTMLInputElement).value })" />
    </label>
    <p v-if="nameError" role="alert" class="mt-1 text-xs text-red-600 dark:text-red-300">{{ nameError }}</p>
    <div class="mt-4 space-y-4">
      <KeywordEditor :model-value="policy.topic_rules" data-test="combination-topic-editor" :label="t('admin.promptAudit.combination.topicRules')" limit-label="" :count-label="t('admin.promptAudit.policy.keywordCount', { count: effectiveTopics.length })" :error="ruleError(effectiveTopics)" @update:model-value="patch({ topic_rules: $event })" />
      <KeywordEditor :model-value="policy.intent_rules" data-test="combination-intent-editor" :label="t('admin.promptAudit.combination.intentRules')" limit-label="" :count-label="t('admin.promptAudit.policy.keywordCount', { count: effectiveIntents.length })" :error="ruleError(effectiveIntents)" @update:model-value="patch({ intent_rules: $event })" />
      <KeywordKeyWhitelist
        :model-value="policy.keyword_whitelist_keys ?? []"
        scope="combination"
        class="border-t border-gray-100 pt-4 dark:border-dark-800"
        data-test="combination-whitelist"
        @update:model-value="patch({ keyword_whitelist_keys: $event })"
      />
      <details :open="Boolean(policy.migrate_keywords?.length)" data-test="combination-migration">
        <summary class="cursor-pointer text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('admin.promptAudit.combination.migrateTitle') }}</summary>
        <p class="my-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.combination.migrateHint') }}</p>
        <KeywordEditor :model-value="policy.migrate_keywords ?? []" data-test="combination-migrate-editor" :label="t('admin.promptAudit.combination.migrateRules')" :limit-label="t('admin.promptAudit.combination.migrateLimit')" :count-label="t('admin.promptAudit.policy.keywordCount', { count: effectiveMigration.length })" :error="migrationError" @update:model-value="patch({ migrate_keywords: $event })" />
      </details>
    </div>
  </fieldset>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PromptCombinationPolicy } from '../types'
import { keywordRuleLengthError, normalizeKeywordRules } from '../keywordRules'
import KeywordEditor from './KeywordEditor.vue'
import KeywordKeyWhitelist from './KeywordKeyWhitelist.vue'

const props = defineProps<{ policy: PromptCombinationPolicy; canMoveUp: boolean; canMoveDown: boolean }>()
const emit = defineEmits<{ (event: 'update:policy', value: PromptCombinationPolicy): void; (event: 'remove'): void; (event: 'move-up'): void; (event: 'move-down'): void }>()
const { t } = useI18n()
const effectiveTopics = computed(() => normalizeKeywordRules(props.policy.topic_rules ?? []))
const effectiveIntents = computed(() => normalizeKeywordRules(props.policy.intent_rules ?? []))
const effectiveMigration = computed(() => normalizeKeywordRules(props.policy.migrate_keywords ?? []))
const nameError = computed(() => !props.policy.name.trim() || Array.from(props.policy.name.trim()).length > 80 || /\p{Cc}/u.test(props.policy.name.trim()) ? t('admin.promptAudit.errors.prompt_audit_invalid_combination_policy_name') : '')
const migrationError = computed(() => effectiveMigration.value.some((rule) => /^regex:/i.test(rule) || Array.from(rule).length > 200) ? t('admin.promptAudit.errors.prompt_audit_invalid_combination_migration_keyword') : '')
function patch(value: Partial<PromptCombinationPolicy>) { emit('update:policy', { ...props.policy, ...value }) }
function ruleError(rules: string[]): string {
  if (props.policy.enabled && rules.length === 0) return t('admin.promptAudit.errors.prompt_audit_combination_rules_required')
  const errorCode = keywordRuleLengthError(rules)
  return errorCode ? t(`admin.promptAudit.errors.${errorCode}`) : ''
}
</script>
