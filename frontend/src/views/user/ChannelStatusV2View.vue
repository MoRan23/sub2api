<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="tabs" role="tablist" :aria-label="t('channelMonitorV2.overview.viewLabel')">
        <button
          v-for="mode in modes"
          :id="`monitor-view-${mode}`"
          :key="mode"
          type="button"
          role="tab"
          class="tab"
          :class="{ 'tab-active': view === mode }"
          :aria-selected="view === mode"
          aria-controls="monitor-view-panel"
          @click="view = mode"
        >{{ t(`channelMonitorV2.overview.${mode}`) }}</button>
      </div>
      <div id="monitor-view-panel" role="tabpanel" :aria-labelledby="`monitor-view-${view}`">
        <GroupOverview v-if="view === 'groups'" />
        <ChannelStatusV2Details v-else />
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { defineAsyncComponent, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import GroupOverview from '@/features/channel-monitor-v2/GroupOverview.vue'

// Detailed endpoints and charts are loaded only when this view is selected.
const ChannelStatusV2Details = defineAsyncComponent(() => import('./ChannelStatusV2Details.vue').then(module => module.default))
const modes = ['groups', 'details'] as const
const view = ref<(typeof modes)[number]>('groups')
const { t } = useI18n()
</script>
