<template>
  <div data-testid="openai-os-profiles" class="mt-4 space-y-3">
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.osProfilesDescription') }}</p>
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
        <button type="button" class="btn btn-secondary text-xs" :disabled="identityBusy || !profiles?.profiles?.[os]" :data-testid="`openai-os-default-${os}`" @click="$emit('set-default', os)">{{ t('admin.accounts.openai.setDefaultOS') }}</button>
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

defineProps<{ profiles?: OpenAIOAuthOSProfiles; creating?: boolean; inherited?: boolean; disabled?: boolean; identityBusy?: boolean; regenerating?: OpenAIOAuthOS | null }>()
defineEmits<{ regenerate: [os: OpenAIOAuthOS]; 'set-default': [os: OpenAIOAuthOS] }>()
const { t } = useI18n()
const operatingSystems: OpenAIOAuthOS[] = ['windows', 'macos', 'linux']
const osLabels = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
</script>
