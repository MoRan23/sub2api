<template>
  <fieldset :data-test="dataTest">
    <legend class="text-sm font-medium text-gray-900 dark:text-white">{{ title }}</legend>
    <div class="mt-3 flex flex-wrap gap-5 text-sm text-gray-700 dark:text-dark-200">
      <label class="flex items-center gap-2">
        <input type="radio" :name="name" :checked="allGroups" @change="allGroups = true" />
        {{ t('admin.promptAudit.policy.allGroups') }}
      </label>
      <label class="flex items-center gap-2">
        <input type="radio" :name="name" :checked="!allGroups" @change="allGroups = false" />
        {{ t('admin.promptAudit.policy.selectedGroups') }}
      </label>
    </div>

    <div v-if="!allGroups" class="mt-4">
      <label class="block text-sm text-gray-700 dark:text-dark-200">
        <span>{{ t('admin.promptAudit.policy.searchGroups') }}</span>
        <input v-model="groupSearch" type="search" class="input mt-1.5 w-full" :aria-label="searchAriaLabel" />
      </label>
      <div class="mt-3 max-h-52 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-dark-700">
        <label v-for="group in filteredGroups" :key="group.id" class="flex cursor-pointer items-center justify-between gap-3 rounded-md px-2 py-2 text-sm hover:bg-gray-50 dark:hover:bg-dark-800">
          <span class="flex items-center gap-2 text-gray-800 dark:text-dark-100">
            <input type="checkbox" :checked="groupIds.includes(group.id)" @change="toggleGroup(group.id)" />
            {{ group.name }}
          </span>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ group.platform }} · {{ group.status }}</span>
        </label>
        <p v-if="filteredGroups.length === 0" class="px-2 py-4 text-center text-sm text-gray-500">{{ t('admin.promptAudit.policy.noGroups') }}</p>
      </div>
      <div v-if="missingGroupIds.length" class="mt-3 rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">
        <p>{{ t('admin.promptAudit.policy.missingGroups') }}</p>
        <div class="mt-2 flex flex-wrap gap-2">
          <span v-for="id in missingGroupIds" :key="id" class="inline-flex items-center gap-1.5">
            <span>{{ id }}</span>
            <button type="button" class="text-xs font-medium underline underline-offset-2" :aria-label="t('admin.promptAudit.policy.removeMissingGroup', { id })" @click="removeGroup(id)">
              {{ t('common.delete') }}
            </button>
          </span>
        </div>
      </div>
      <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.policy.selectedCount', { count: groupIds.length }) }}</p>
    </div>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PromptAuditGroup } from '../types'

const props = defineProps<{
  title: string
  name: string
  groups: PromptAuditGroup[]
  searchAriaLabel: string
  dataTest?: string
}>()
const allGroups = defineModel<boolean>('allGroups', { required: true })
const groupIds = defineModel<number[]>('groupIds', { required: true })
const { t } = useI18n()
const groupSearch = ref('')

const filteredGroups = computed(() => {
  const query = groupSearch.value.trim().toLowerCase()
  if (!query) return props.groups
  return props.groups.filter((group) => `${group.name} ${group.id} ${group.platform}`.toLowerCase().includes(query))
})
const knownGroupIds = computed(() => new Set(props.groups.map((group) => group.id)))
const missingGroupIds = computed(() => groupIds.value.filter((id) => !knownGroupIds.value.has(id)))

function toggleGroup(id: number) {
  const selected = new Set(groupIds.value)
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  groupIds.value = [...selected].sort((a, b) => a - b)
}

function removeGroup(id: number) {
  groupIds.value = groupIds.value.filter((groupId) => groupId !== id)
}
</script>
