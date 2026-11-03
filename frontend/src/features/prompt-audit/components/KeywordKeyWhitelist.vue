<template>
  <div>
    <h3 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.promptAudit.keywordWhitelist.title') }}</h3>
    <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.keywordWhitelist.description') }}</p>
    <div class="mt-3 flex items-end gap-2">
      <label class="min-w-0 flex-1 text-sm text-gray-700 dark:text-dark-200">
        {{ t('admin.promptAudit.keywordWhitelist.searchUsers') }}
        <input v-model="search" class="input mt-1 w-full" :aria-label="t('admin.promptAudit.keywordWhitelist.searchUsers')" data-test="search-users" @keydown.enter.prevent="loadUsers(1)" />
      </label>
      <button type="button" class="btn btn-secondary" data-test="search" :disabled="usersLoading" @click="loadUsers(1)">{{ t('common.search') }}</button>
    </div>
    <p v-if="usersLoading" class="mt-2 text-xs" role="status">{{ t('common.loading') }}</p>
    <div v-if="usersSearched" class="mt-3">
      <label class="block text-sm text-gray-700 dark:text-dark-200">
        {{ t('admin.promptAudit.keywordWhitelist.user') }}
        <select v-model.number="selectedUserId" class="input mt-1 w-full" data-test="user" :aria-label="t('admin.promptAudit.keywordWhitelist.user')" :disabled="usersLoading">
          <option :value="0">{{ t('admin.promptAudit.keywordWhitelist.selectUser') }}</option>
          <option v-for="user in users" :key="user.id" :value="user.id">{{ user.email }} · #{{ user.id }}</option>
        </select>
      </label>
      <p v-if="!usersLoading && !users.length" class="mt-1 text-xs text-gray-500">{{ t('admin.promptAudit.keywordWhitelist.noUsers') }}</p>
      <div v-if="usersTotal > pageSize" class="mt-2 flex items-center gap-2 text-xs">
        <button type="button" class="btn btn-secondary btn-sm" data-test="users-prev" :disabled="usersLoading || usersPage <= 1" @click="loadUsers(usersPage - 1)">{{ t('admin.promptAudit.keywordWhitelist.previous') }}</button>
        <span>{{ usersPage }} / {{ Math.ceil(usersTotal / pageSize) }}</span>
        <button type="button" class="btn btn-secondary btn-sm" data-test="users-next" :disabled="usersLoading || usersPage * pageSize >= usersTotal" @click="loadUsers(usersPage + 1)">{{ t('admin.promptAudit.keywordWhitelist.next') }}</button>
      </div>
    </div>
    <div v-if="selectedUserId" class="mt-3">
      <label class="block text-sm text-gray-700 dark:text-dark-200">
        {{ t('admin.promptAudit.keywordWhitelist.key') }}
        <select v-model.number="selectedKeyId" class="input mt-1 w-full" data-test="key" :aria-label="t('admin.promptAudit.keywordWhitelist.key')" :disabled="keysLoading">
          <option :value="0">{{ t(keysLoading ? 'common.loading' : 'admin.promptAudit.keywordWhitelist.selectKey') }}</option>
          <option v-for="key in keys" :key="key.id" :value="key.id">{{ key.name || t('admin.promptAudit.keywordWhitelist.unnamedKey') }} · #{{ key.id }}</option>
        </select>
      </label>
      <p v-if="!keysLoading && !keys.length && !error" class="mt-1 text-xs text-gray-500">{{ t('admin.promptAudit.keywordWhitelist.noKeys') }}</p>
      <div v-if="keysTotal > pageSize" class="mt-2 flex items-center gap-2 text-xs">
        <button type="button" class="btn btn-secondary btn-sm" data-test="keys-prev" :disabled="keysLoading || keysPage <= 1" @click="loadKeys(keysPage - 1)">{{ t('admin.promptAudit.keywordWhitelist.previous') }}</button>
        <span>{{ keysPage }} / {{ Math.ceil(keysTotal / pageSize) }}</span>
        <button type="button" class="btn btn-secondary btn-sm" data-test="keys-next" :disabled="keysLoading || keysPage * pageSize >= keysTotal" @click="loadKeys(keysPage + 1)">{{ t('admin.promptAudit.keywordWhitelist.next') }}</button>
      </div>
      <button type="button" class="btn btn-secondary mt-3" data-test="add-key" :disabled="!canAdd" @click="addKey">{{ t('admin.promptAudit.keywordWhitelist.add') }}</button>
    </div>
    <p v-if="error" class="mt-2 text-sm text-red-600 dark:text-red-400" role="alert">{{ error }}</p>
    <p v-if="modelValue.length >= 1000" class="mt-2 text-xs text-amber-600">{{ t('admin.promptAudit.errors.prompt_audit_too_many_keyword_whitelist_keys') }}</p>
    <ul v-if="modelValue.length" class="mt-4 space-y-2">
      <li v-for="entry in modelValue" :key="entryId(entry)" class="flex items-center justify-between gap-3 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-900/50" data-test="saved-entry">
        <div class="min-w-0 break-words">
          <p v-if="identities[entryId(entry)]?.available" class="font-medium">
            {{ identityLabel(identities[entryId(entry)]) }} · {{ identities[entryId(entry)].key_name || t('admin.promptAudit.keywordWhitelist.unnamedKey') }}
          </p>
          <p v-else-if="identitiesLoading" role="status">{{ t('admin.promptAudit.keywordWhitelist.metadataLoading') }}</p>
          <p v-else-if="identitiesFailed" class="text-amber-600 dark:text-amber-400">{{ t('admin.promptAudit.keywordWhitelist.metadataLoadFailed') }}</p>
          <p v-else class="text-amber-600 dark:text-amber-400">{{ t('admin.promptAudit.keywordWhitelist.missingIdentity') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.keywordWhitelist.identity', { userId: entry.user_id, keyId: entry.api_key_id }) }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm shrink-0" data-test="remove-key" :aria-label="t('admin.promptAudit.keywordWhitelist.remove', { userId: entry.user_id, keyId: entry.api_key_id })" @click="removeKey(entry)">{{ t('common.remove') }}</button>
      </li>
    </ul>
    <p v-else class="mt-3 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.keywordWhitelist.empty') }}</p>
    <button v-if="modelValue.length && identitiesFailed" type="button" class="btn btn-secondary btn-sm mt-2" data-test="retry-identities" @click="loadIdentities">{{ t('admin.promptAudit.actions.retry') }}</button>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getKeywordWhitelistIdentities, listKeywordWhitelistKeys, listKeywordWhitelistUsers } from '../api'
import type { PromptKeywordKeyOption, PromptKeywordUserOption, PromptKeywordWhitelistIdentity, PromptKeywordWhitelistKey } from '../types'

const props = defineProps<{ modelValue: PromptKeywordWhitelistKey[] }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: PromptKeywordWhitelistKey[]): void }>()
const { t } = useI18n()
const pageSize = 20
const search = ref('')
const users = ref<PromptKeywordUserOption[]>([])
const usersPage = ref(1)
const usersTotal = ref(0)
const usersSearched = ref(false)
const usersLoading = ref(false)
const keys = ref<PromptKeywordKeyOption[]>([])
const keysPage = ref(1)
const keysTotal = ref(0)
const keysLoading = ref(false)
const selectedUserId = ref(0)
const selectedKeyId = ref(0)
const error = ref('')
const identities = ref<Record<string, PromptKeywordWhitelistIdentity>>({})
const identitiesLoading = ref(false)
const identitiesFailed = ref(false)
let userRequest = 0
let keyRequest = 0
let identityRequest = 0

const selectedUser = computed(() => users.value.find((user) => user.id === selectedUserId.value))
const selectedKey = computed(() => keys.value.find((key) => key.id === selectedKeyId.value && key.user_id === selectedUserId.value))
const canAdd = computed(() => !usersLoading.value && !keysLoading.value && selectedUser.value && selectedKey.value && props.modelValue.length < 1000 && !props.modelValue.some((entry) => entry.user_id === selectedUserId.value && entry.api_key_id === selectedKeyId.value))
const entryId = (entry: PromptKeywordWhitelistKey) => `${entry.user_id}:${entry.api_key_id}`
const identityLabel = (identity: PromptKeywordWhitelistIdentity) => [identity.user_email, identity.username].filter(Boolean).join(' / ')

// Watch IDs only: a cloned settings draft does not need another identity lookup.
watch(() => props.modelValue.map(entryId).join(','), () => { void loadIdentities() }, { immediate: true })

async function loadIdentities() {
  const request = ++identityRequest
  const keys = props.modelValue.map(({ user_id, api_key_id }) => ({ user_id, api_key_id }))
  const retained = Object.fromEntries(keys.flatMap((key) => {
    const identity = identities.value[entryId(key)]
    return identity ? [[entryId(key), identity]] : []
  }))
  identities.value = retained
  identitiesFailed.value = false
  identitiesLoading.value = keys.length > 0
  if (!keys.length) return
  try {
    const result = await getKeywordWhitelistIdentities(keys)
    if (request !== identityRequest) return
    const requested = new Set(keys.map(entryId))
    identities.value = Object.fromEntries(result.filter((identity) => requested.has(entryId(identity))).map((identity) => [entryId(identity), identity]))
  } catch {
    if (request === identityRequest) {
      identities.value = {}
      identitiesFailed.value = true
    }
  } finally {
    if (request === identityRequest) identitiesLoading.value = false
  }
}

function clearKeys() {
  keyRequest++
  selectedKeyId.value = 0
  keys.value = []
  keysPage.value = 1
  keysTotal.value = 0
  keysLoading.value = false
}

watch(search, () => {
  userRequest++
  users.value = []
  usersTotal.value = 0
  usersSearched.value = false
  usersLoading.value = false
  selectedUserId.value = 0
  clearKeys()
  error.value = ''
})
watch(selectedUserId, () => { clearKeys(); if (selectedUser.value) void loadKeys(1) })
onBeforeUnmount(() => { userRequest++; keyRequest++; identityRequest++ })

async function loadUsers(page: number) {
  const request = ++userRequest
  selectedUserId.value = 0
  clearKeys()
  users.value = []
  usersLoading.value = true
  usersSearched.value = true
  error.value = ''
  try {
    const result = await listKeywordWhitelistUsers(search.value.trim(), page)
    if (request !== userRequest) return
    users.value = result.items
    usersPage.value = page
    usersTotal.value = result.total
  } catch {
    if (request === userRequest) error.value = t('admin.promptAudit.keywordWhitelist.loadFailed')
  } finally {
    if (request === userRequest) usersLoading.value = false
  }
}

async function loadKeys(page: number) {
  const userId = selectedUserId.value
  const request = ++keyRequest
  selectedKeyId.value = 0
  keys.value = []
  keysLoading.value = true
  error.value = ''
  try {
    const result = await listKeywordWhitelistKeys(userId, page)
    if (request !== keyRequest || selectedUserId.value !== userId) return
    keys.value = result.items.filter((key) => key.user_id === userId)
    keysPage.value = page
    keysTotal.value = result.total
  } catch {
    if (request === keyRequest) error.value = t('admin.promptAudit.keywordWhitelist.loadFailed')
  } finally {
    if (request === keyRequest) keysLoading.value = false
  }
}

function addKey() {
  if (!canAdd.value || !selectedUser.value || !selectedKey.value) return
  const entry = { user_id: selectedUser.value.id, api_key_id: selectedKey.value.id }
  identities.value[entryId(entry)] = { ...entry, user_email: selectedUser.value.email, username: '', key_name: selectedKey.value.name, available: true }
  emit('update:modelValue', [...props.modelValue.map((value) => ({ ...value })), entry])
  selectedKeyId.value = 0
}

function removeKey(entry: PromptKeywordWhitelistKey) {
  emit('update:modelValue', props.modelValue.filter((value) => entryId(value) !== entryId(entry)).map((value) => ({ ...value })))
}
</script>
