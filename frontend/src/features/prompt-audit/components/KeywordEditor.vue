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
import { computed } from 'vue'

const { label, placeholder = '', disabled = false, error = '', countLabel, limitLabel } = defineProps<{
  label: string
  placeholder?: string
  disabled?: boolean
  error?: string
  countLabel: string
  limitLabel: string
}>()

const keywords = defineModel<string[]>({ required: true })
const text = computed({
  get: () => keywords.value.join('\n'),
  set: (value: string) => {
    const seen = new Set<string>()
    keywords.value = value.split(/\r?\n/).map((line) => line.trim()).filter((line) => {
      if (!line) return false
      const key = line.toLowerCase()
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
  },
})
</script>
