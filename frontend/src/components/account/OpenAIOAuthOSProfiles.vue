<template>
  <div data-testid="openai-os-profiles" class="mt-4 space-y-3">
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.osProfilesDescription') }}</p>
    <section v-if="!creating" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="openai-account-authorization">
      <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-200">{{ t('admin.accounts.openai.accountAuthorization') }}</h4>
      <p class="mt-2 text-sm" :class="authorization?.status === 'authorized' ? 'text-green-700 dark:text-green-400' : 'text-amber-700 dark:text-amber-400'">{{ t(`admin.accounts.openai.authorizationStatus.${authorization?.status || 'unauthorized'}`) }}</p>
      <p v-if="authorization?.last_error" class="mt-2 text-xs text-amber-700 dark:text-amber-400">{{ authorization.last_error }}</p>
      <p v-if="authorization?.expires_at" class="mt-2 text-xs text-gray-500">{{ t('admin.accounts.openai.authorizationExpiresAt') }}: {{ authorization.expires_at }}</p>
      <div v-if="!inherited" class="mt-3 flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary text-xs" :disabled="authorizationBusy" data-testid="openai-account-authorize" @click="$emit('authorize')">{{ t(`admin.accounts.openai.${!authorization || authorization.status === 'unauthorized' ? 'authorize' : 'reauthorize'}`) }}</button>
        <button v-if="authorization && authorization.status !== 'unauthorized'" type="button" class="btn btn-secondary text-xs" :disabled="authorizationBusy" data-testid="openai-account-revoke" @click="$emit('revoke')">{{ t('admin.accounts.openai.revokeAuthorization') }}</button>
      </div>
    </section>
    <p class="text-sm text-gray-700 dark:text-gray-200">
      {{ t('admin.accounts.openai.defaultOS') }}: {{ profiles?.default_os ? osLabels[profiles.default_os] : creating ? osLabels.windows : '—' }}
    </p>
    <section v-for="os in operatingSystems" :key="os" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" :data-testid="`openai-os-profile-${os}`">
      <div class="mb-2 flex items-center justify-between gap-2">
        <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-200">{{ osLabels[os] }}</h4>
        <button v-if="!creating && !inherited" type="button" class="btn btn-secondary text-xs" :disabled="disabled || !!regenerating || !profiles?.profiles?.[os]" :data-testid="`openai-installation-regenerate-${os}`" @click="$emit('regenerate', os)">
          <Icon name="refresh" size="sm" :class="regenerating === os ? 'animate-spin' : ''" />
          {{ t('admin.accounts.openai.installationRegenerate') }}
        </button>
      </div>
      <div v-if="!creating && !inherited && profiles?.default_os !== os" class="mb-3">
        <button type="button" class="btn btn-secondary text-xs" :disabled="authorizationBusy || !profiles?.profiles?.[os]" :data-testid="`openai-os-default-${os}`" @click="$emit('set-default', os)">{{ t('admin.accounts.openai.setDefaultOS') }}</button>
      </div>
      <dl class="space-y-2 text-xs">
        <div><dt class="text-gray-500 dark:text-gray-400">installation_id</dt><dd class="mt-1 select-text break-all font-mono text-gray-700 dark:text-gray-200">{{ profiles?.profiles?.[os]?.installation_id || t(`admin.accounts.openai.${creating ? 'osProfilePending' : 'osProfileUnavailable'}`) }}</dd></div>
        <div><dt class="text-gray-500 dark:text-gray-400">User-Agent</dt><dd class="mt-1 select-text break-all font-mono text-gray-700 dark:text-gray-200">{{ profiles?.profiles?.[os]?.user_agent || t(`admin.accounts.openai.${creating ? 'osProfilePending' : 'osProfileUnavailable'}`) }}</dd></div>
      </dl>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { OpenAIOAuthOS, OpenAIOAuthOSProfiles } from '@/types'
import { openAIAccountAuthorization } from './openaiOAuthOS'

const props = defineProps<{ profiles?: OpenAIOAuthOSProfiles; creating?: boolean; inherited?: boolean; disabled?: boolean; authorizationBusy?: boolean; regenerating?: OpenAIOAuthOS | null }>()
defineEmits<{ regenerate: [os: OpenAIOAuthOS]; authorize: []; revoke: []; 'set-default': [os: OpenAIOAuthOS] }>()
const authorization = computed(() => openAIAccountAuthorization(props.profiles))
const { t } = useI18n()
const operatingSystems: OpenAIOAuthOS[] = ['windows', 'macos', 'linux']
const osLabels = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
</script>
