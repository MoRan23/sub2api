<template>
  <div>
    <label class="block text-sm text-gray-700 dark:text-dark-200">
      <span>{{ label }}</span>
      <textarea
        v-model="text"
        :disabled="disabled"
        :aria-label="label"
        :placeholder="placeholder"
        rows="6"
        class="input mt-1.5 w-full resize-y font-mono text-sm"
      />
    </label>
    <p v-if="error" class="mt-1.5 text-xs text-red-600 dark:text-red-300" role="alert">{{ error }}</p>
    <div class="mt-2 flex flex-wrap items-center justify-between gap-2 text-xs text-gray-500 dark:text-dark-400">
      <span>{{ countLabel }}</span>
      <span>{{ limitLabel }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { normalizeKeywordRules } from '../keywordRules'

const { label, placeholder = '', disabled = false, error = '', countLabel, limitLabel } = defineProps<{
  label: string
  placeholder?: string
  disabled?: boolean
  error?: string
  countLabel: string
  limitLabel: string
}>()

const keywords = defineModel<string[]>({ required: true })
const draftText = ref(keywords.value.join('\n'))
const text = computed({
  get: () => draftText.value,
  set: (value: string) => {
    draftText.value = value
    const normalized = normalizeLines(value)
    if (!sameKeywords(normalized, keywords.value)) keywords.value = normalized
  },
})

watch(keywords, (value) => {
  // Preserve the editing draft when the parent echoes normalized keywords.
  if (!sameKeywords(value, normalizeLines(draftText.value))) draftText.value = value.join('\n')
}, { deep: true })

function normalizeLines(value: string): string[] {
  return normalizeKeywordRules(value.split(/\r?\n/))
}

function sameKeywords(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index])
}
</script>
