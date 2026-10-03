<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6 p-6">
      <div><h1 class="text-2xl font-semibold">{{ t('attribution.title') }}</h1><p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ t('attribution.description') }}</p></div>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950 dark:text-red-300">{{ error }}</p>
      <p v-if="message" role="status" class="text-sm text-primary-600 dark:text-primary-400">{{ message }}</p>
      <form v-if="config" class="space-y-6" @submit.prevent="save">
        <fieldset :disabled="saving" class="space-y-6">
          <section class="space-y-4 rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-600 dark:bg-dark-800">
            <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="font-semibold">{{ t('attribution.global') }}</h2><label class="flex items-center gap-2 text-sm"><input v-model="config.enabled" type="checkbox" data-testid="attribution-enable" />{{ t('attribution.enabled') }}</label></div>
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('attribution.notice') }}</p>
            <label class="block text-sm font-medium">{{ t('attribution.service') }}<input v-model="config.base_url" type="url" class="input mt-2 w-full" placeholder="http://modeltrace:5000" data-testid="attribution-url" /></label>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="connecting || !config.base_url" @click="connect">{{ connecting ? t('attribution.loading') : t('attribution.connect') }}</button>
            <p v-if="candidates.length" class="break-all text-xs text-gray-500">{{ t('attribution.candidates') }}: {{ candidates.join(', ') }}</p>
            <datalist id="attribution-models"><option v-for="id in candidates" :key="id" :value="id" /></datalist>
            <AttributionPolicyFields v-model="config.default" />
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.mappingHelp') }}</p>
            <div class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="global-group-priority">
              <h3 class="text-sm font-medium">{{ t('attribution.priority.title') }}</h3>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.priority.help') }}</p>
              <div class="flex gap-3"><select v-model="priorityGroup" class="input flex-1" :aria-label="t('attribution.priority.title')" data-testid="priority-group"><option :value="0">{{ t('attribution.group') }}</option><option v-for="group in priorityAvailableGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select><button type="button" class="btn btn-secondary" :disabled="!priorityGroup" data-testid="priority-add" @click="addPriority">{{ t('attribution.priority.add') }}</button></div>
              <ol class="space-y-2">
                <li v-for="(id, index) in config.group_priority" :key="id" class="flex items-center gap-3 text-sm" data-testid="priority-row">
                  <span class="min-w-0 flex-1">{{ index + 1 }}. {{ groups.find(g => g.id === id)?.name || `#${id}` }}</span>
                  <button type="button" class="btn btn-secondary btn-sm" :disabled="index === 0" data-testid="priority-up" @click="movePriority(index, -1)">{{ t('attribution.priority.up') }}</button>
                  <button type="button" class="btn btn-secondary btn-sm" :disabled="index === config.group_priority.length - 1" data-testid="priority-down" @click="movePriority(index, 1)">{{ t('attribution.priority.down') }}</button>
                  <button type="button" class="text-xs text-red-600 dark:text-red-400" @click="config.group_priority.splice(index, 1)">{{ t('attribution.remove') }}</button>
                </li>
              </ol>
            </div>
          </section>
          <section class="space-y-4 rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-600 dark:bg-dark-800" data-testid="new-account-tests">
            <h2 class="font-semibold">{{ t('attribution.newAccount.title') }}</h2>
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('attribution.newAccount.help') }}</p>
            <label class="flex items-center gap-2 text-sm"><input v-model="config.new_account_tests.attribution" type="checkbox" data-testid="initial-attribution" />{{ t('attribution.newAccount.attribution') }}</label>
            <p v-if="config.new_account_tests.attribution && !config.enabled" class="text-xs text-amber-700 dark:text-amber-300">{{ t('attribution.newAccount.requiresEnabled') }}</p>
            <label class="flex items-center gap-2 text-sm"><input v-model="config.new_account_tests.pelican" type="checkbox" data-testid="initial-pelican" />{{ t('attribution.newAccount.pelican') }}</label>
            <label class="block text-sm font-medium">{{ t('attribution.newAccount.model') }}<input v-model="config.new_account_tests.model" list="attribution-models" class="input mt-2 w-full" placeholder="gpt-6-astra" maxlength="200" data-testid="initial-model" /></label>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('attribution.newAccount.modelHelp') }}</p>
          </section>
          <section class="space-y-4 rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-600 dark:bg-dark-800">
            <h2 class="font-semibold">{{ t('attribution.groupTitle') }}</h2><p class="text-sm text-gray-500 dark:text-gray-400">{{ t('attribution.groupHelp') }}</p>
            <div class="flex gap-3"><select v-model="newGroup" class="input flex-1" :aria-label="t('attribution.group')"><option :value="0">{{ t('attribution.group') }}</option><option v-for="group in availableGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select><button type="button" class="btn btn-secondary" :disabled="!newGroup" @click="addGroup">{{ t('attribution.addGroup') }}</button></div>
            <div v-for="override in config.groups" :key="override.group_id" class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
              <div class="flex flex-wrap items-center justify-between gap-3"><h3 class="text-sm font-medium">{{ groups.find(g => g.id === override.group_id)?.name || `#${override.group_id}` }}</h3><button type="button" class="text-xs text-red-600 dark:text-red-400" @click="config.groups = config.groups.filter(g => g !== override)">{{ t('attribution.remove') }}</button></div>
              <label class="flex items-center gap-2 text-sm"><input v-model="override.enabled" type="checkbox" />{{ override.enabled ? t('attribution.independent') : t('attribution.inherit') }}</label>
              <AttributionPolicyFields v-if="override.enabled" :model-value="override" @update:model-value="Object.assign(override, $event)" />
            </div>
          </section>
          <div class="flex flex-wrap items-center gap-3"><button type="submit" class="btn btn-primary" data-testid="attribution-save">{{ saving ? t('attribution.loading') : t('attribution.save') }}</button><button type="button" class="btn btn-secondary" @click="load">{{ t('attribution.reload') }}</button><span class="text-xs text-gray-500">{{ t('attribution.version') }} {{ config.version }}</span></div>
        </fieldset>
      </form>
      <AttributionHistory />
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import AttributionPolicyFields from '@/components/admin/account/AttributionPolicyFields.vue'
import AttributionHistory from '@/components/admin/account/AttributionHistory.vue'
import { attributionAPI, type AttributionConfig, type AttributionPolicy } from '@/api/admin/modelAttribution'
import { getAllIncludingInactive } from '@/api/admin/groups'
import type { AdminGroup } from '@/types'
const { t } = useI18n()
const config = ref<AttributionConfig>()
const groups = ref<AdminGroup[]>([])
const candidates = ref<string[]>([])
const newGroup = ref(0)
const priorityGroup = ref(0)
const saving = ref(false)
const connecting = ref(false)
const error = ref('')
const message = ref('')
const availableGroups = computed(() => groups.value.filter(g => !config.value?.groups.some(o => o.group_id === g.id)))
const priorityAvailableGroups = computed(() => groups.value.filter(g => !config.value?.group_priority.includes(g.id)))
function addPriority() {
  if (!config.value || !priorityGroup.value) return
  config.value.group_priority.push(priorityGroup.value)
  priorityGroup.value = 0
}
function movePriority(index: number, delta: number) {
  const ids = config.value?.group_priority
  if (!ids || index + delta < 0 || index + delta >= ids.length) return
  const [id] = ids.splice(index, 1)
  ids.splice(index + delta, 0, id)
}
async function load() {
  error.value = ''; message.value = ''
  try { const [c, g] = await Promise.all([attributionAPI.config(), getAllIncludingInactive()]); config.value = { ...c, group_priority: c.group_priority || [] }; groups.value = g }
  catch { error.value = t('attribution.error') }
}
function addGroup() {
  if (!config.value || !newGroup.value) return
  const policy = config.value.default
  config.value.groups.push({ group_id: newGroup.value, enabled: false, model: policy.model, high_models: [...policy.high_models], low_models: [...policy.low_models] })
  newGroup.value = 0
}
function validPolicy(p: AttributionPolicy) {
  return [p.model, ...p.high_models, ...p.low_models].every(m => m.trim().length > 0 && !/[\s*]/.test(m)) && p.high_models.length > 0 && p.low_models.length > 0
}
async function save() {
  if (!config.value) return
  error.value = ''; message.value = ''
  const initialModel = config.value.new_account_tests.model.trim()
  if (!initialModel || initialModel.length > 200 || /[\s*]/.test(initialModel)) { error.value = t('attribution.newAccount.invalid'); return }
  if (config.value.enabled && (!/^https?:\/\//.test(config.value.base_url) || !validPolicy(config.value.default) || config.value.groups.some(g => g.enabled && !validPolicy(g)))) { error.value = t('attribution.invalid'); return }
  saving.value = true
  try { config.value = await attributionAPI.save(config.value); message.value = t('attribution.saved') }
  catch (e) { error.value = e && typeof e === 'object' && 'status' in e && e.status === 409 ? t('attribution.conflict') : t('attribution.error') }
  finally { saving.value = false }
}
async function connect() {
  if (!config.value) return
  connecting.value = true; error.value = ''; candidates.value = []
  try { candidates.value = await attributionAPI.models(config.value.base_url) }
  catch { error.value = t('attribution.reasons.modeltrace_unavailable') }
  finally { connecting.value = false }
}
onMounted(load)
</script>
