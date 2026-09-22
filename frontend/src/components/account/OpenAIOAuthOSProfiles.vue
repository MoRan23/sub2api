<template>
  <div data-testid="openai-os-profiles" class="mt-4 space-y-3">
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.osProfilesDescription') }}</p>
    <p class="text-sm text-gray-700 dark:text-gray-200">
      {{ t('admin.accounts.openai.defaultOS') }}: {{ profiles?.default_os ? osLabels[profiles.default_os] : creating ? osLabels[initialOS || 'windows'] : '—' }}
    </p>
    <section v-for="os in operatingSystems" :key="os" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" :data-testid="`openai-os-profile-${os}`">
      <div class="mb-2 flex items-center justify-between gap-2">
        <h4 class="text-sm font-semibold text-gray-800 dark:text-gray-200">{{ osLabels[os] }}</h4>
        <button v-if="!creating && !inherited" type="button" class="btn btn-secondary text-xs" :disabled="disabled || !!regenerating || !profiles?.profiles?.[os]" :data-testid="`openai-installation-regenerate-${os}`" @click="$emit('regenerate', os)">
          <Icon name="refresh" size="sm" :class="regenerating === os ? 'animate-spin' : ''" />
          {{ t('admin.accounts.openai.installationRegenerate') }}
        </button>
      </div>
      <p class="mb-2 text-sm" :class="isOpenAIOSAuthorized(profiles, os) ? 'text-green-700 dark:text-green-400' : 'text-amber-700 dark:text-amber-400'" :data-testid="`openai-os-authorization-${os}`">
        {{ t(`admin.accounts.openai.authorizationStatus.${profiles?.profiles?.[os]?.authorization?.status || 'unauthorized'}`) }}
      </p>
      <p v-if="profiles?.profiles?.[os]?.authorization?.last_error" class="mb-2 text-xs text-amber-700 dark:text-amber-400">{{ profiles.profiles[os].authorization?.last_error }}</p>
      <p v-if="profiles?.profiles?.[os]?.authorization?.expires_at" class="mb-2 text-xs text-gray-500">{{ t('admin.accounts.openai.authorizationExpiresAt') }}: {{ profiles.profiles[os].authorization?.expires_at }}</p>
      <div v-if="!creating && !inherited" class="mb-3 flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary text-xs" :disabled="authorizationBusy" :data-testid="`openai-os-authorize-${os}`" @click="$emit('authorize', os)">{{ t(`admin.accounts.openai.${profiles?.profiles?.[os]?.authorization?.status === 'unauthorized' || !profiles?.profiles?.[os]?.authorization ? 'authorizeOS' : 'reauthorizeOS'}`) }}</button>
        <button v-if="profiles?.profiles?.[os]?.authorization?.status && profiles.profiles[os].authorization?.status !== 'unauthorized'" type="button" class="btn btn-secondary text-xs" :disabled="authorizationBusy" :data-testid="`openai-os-revoke-${os}`" @click="$emit('revoke', os)">{{ t('admin.accounts.openai.revokeOS') }}</button>
        <button v-if="profiles?.default_os !== os && isOpenAIOSAuthorized(profiles, os)" type="button" class="btn btn-secondary text-xs" :disabled="authorizationBusy" :data-testid="`openai-os-default-${os}`" @click="$emit('set-default', os)">{{ t('admin.accounts.openai.setDefaultOS') }}</button>
      </div>
      <dl class="space-y-2 text-xs">
        <div><dt class="text-gray-500 dark:text-gray-400">installation_id</dt><dd class="mt-1 select-text break-all font-mono text-gray-700 dark:text-gray-200">{{ profiles?.profiles?.[os]?.installation_id || t(`admin.accounts.openai.${creating ? 'osProfilePending' : 'osProfileUnavailable'}`) }}</dd></div>
        <div><dt class="text-gray-500 dark:text-gray-400">User-Agent</dt><dd class="mt-1 select-text break-all font-mono text-gray-700 dark:text-gray-200">{{ profiles?.profiles?.[os]?.user_agent || t(`admin.accounts.openai.${creating ? 'osProfilePending' : 'osProfileUnavailable'}`) }}</dd></div>
      </dl>
    </section>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import type { OpenAIOAuthOS, OpenAIOAuthOSProfiles } from '@/types'
import { isOpenAIOSAuthorized } from './openaiOAuthOS'

defineProps<{ profiles?: OpenAIOAuthOSProfiles; initialOS?: OpenAIOAuthOS; creating?: boolean; inherited?: boolean; disabled?: boolean; authorizationBusy?: boolean; regenerating?: OpenAIOAuthOS | null }>()
defineEmits<{ regenerate: [os: OpenAIOAuthOS]; authorize: [os: OpenAIOAuthOS]; revoke: [os: OpenAIOAuthOS]; 'set-default': [os: OpenAIOAuthOS] }>()
const { t } = useI18n()
const operatingSystems: OpenAIOAuthOS[] = ['windows', 'macos', 'linux']
const osLabels = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
</script>
