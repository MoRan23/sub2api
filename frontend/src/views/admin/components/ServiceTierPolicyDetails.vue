<template>
  <section v-if="observation" :aria-label="t(`${prefix}.title`)" class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700" data-testid="service-tier-policy-details">
    <h3 class="font-semibold text-gray-800 dark:text-gray-200">{{ t(`${prefix}.title`) }}</h3>
    <p class="mt-1 text-gray-500 dark:text-gray-400">{{ t(`${prefix}.description`) }}</p>
    <dl class="mt-3 grid gap-3 sm:grid-cols-3">
      <div>
        <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.source`) }}</dt>
        <dd class="mt-1 text-gray-800 dark:text-gray-200" data-testid="tier-policy-source">{{ t(`${prefix}.sources.${observation.source}`) }}</dd>
      </div>
      <div>
        <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.requested`) }}</dt>
        <dd class="mt-1 font-mono text-gray-800 dark:text-gray-200" data-testid="tier-policy-requested">{{ observation.requested_tier || t(`${prefix}.absent`) }}</dd>
      </div>
      <div>
        <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.forced`) }}</dt>
        <dd class="mt-1 font-mono text-gray-800 dark:text-gray-200" data-testid="tier-policy-forced">{{ observation.forced_tier || t(`${prefix}.none`) }}</dd>
      </div>
      <div>
        <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.effective`) }}</dt>
        <dd class="mt-1 font-mono text-gray-800 dark:text-gray-200" data-testid="tier-policy-effective">{{ observation.effective_tier || t(`${prefix}.absent`) }}</dd>
      </div>
      <div class="sm:col-span-2">
        <dt class="text-gray-500 dark:text-gray-400">{{ t(`${prefix}.capability`) }}</dt>
        <dd class="mt-1 text-gray-800 dark:text-gray-200" data-testid="tier-policy-capability">{{ t(`${prefix}.capabilities.${observation.capability}`) }}</dd>
      </div>
    </dl>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ServiceTierPolicyObservation } from '@/api/admin/fingerprintObservations'

defineProps<{ observation?: ServiceTierPolicyObservation }>()
const { t } = useI18n()
const prefix = 'admin.fingerprintObservation.request.serviceTierPolicy'
</script>
