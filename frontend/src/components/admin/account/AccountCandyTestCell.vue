<template>
  <!-- The zero-height anchor contributes width only. The content is fitted to
       the cell height determined by the OTHER columns, including after resize. -->
  <div ref="anchor" class="relative h-0 w-60 max-w-full" data-testid="pelican-cell">
    <div class="absolute left-0 w-full overflow-hidden" :style="{ top: `${box.top}px`, height: `${box.height}px` }" data-testid="pelican-cell-content">
      <span v-if="account.platform !== 'openai'" class="flex h-full items-center text-gray-400">—</span>
      <button v-else type="button" class="relative block h-full w-full overflow-hidden rounded text-left text-xs focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500" :title="detailsTitle" :aria-label="detailsTitle" @click.stop="emit('open')">
        <template v-if="previewVisible && html">
          <PelicanHTMLThumbnail :key="summary.latest?.id" :html="html" :width="box.width" :height="box.height" :max-scale="layout.scale.value" />
          <span class="sr-only">{{ t('candyTests.generated') }}</span>
          <AccountCandyTestStatus v-if="summary.active" class="absolute right-1 top-1" :status="summary.active.status" />
        </template>
        <div v-else class="flex h-full flex-col justify-center gap-1 overflow-hidden">
          <div class="flex gap-1">
            <AccountCandyTestStatus v-if="summary.latest" :status="summary.latest.status" />
            <span v-else class="text-gray-400">{{ t('candyTests.noResult') }}</span>
            <AccountCandyTestStatus v-if="summary.active" :status="summary.active.status" />
          </div>
          <div v-if="summary.latest" class="truncate text-gray-600 dark:text-gray-300">{{ summary.latest.model }} · {{ summary.latest.reasoning_effort || t('candyTests.defaultEffort') }}</div>
          <div v-if="summary.latest?.status === 'generated'" class="truncate text-gray-400">{{ t(loading ? 'candyTests.previewLoading' : 'candyTests.previewUnavailable') }}</div>
          <div class="truncate text-primary-500">{{ t(summary.active ? 'candyTests.activeBatch' : 'candyTests.details') }}</div>
        </div>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { PELICAN_TEST_PROMPT_VERSION } from '@/api/admin/candyTests'
import type { CandyTestSummary } from '@/api/admin/candyTests'
import { formatDateTime } from '@/utils/format'
import AccountCandyTestStatus from './AccountCandyTestStatus.vue'
import PelicanHTMLThumbnail from './PelicanHTMLThumbnail.vue'
import { loadPelicanThumbnail } from './pelicanThumbnail'
import { createPelicanThumbnailLayout, pelicanThumbnailLayoutKey } from './pelicanThumbnailLayout'

const props = defineProps<{ account: Account & { candy_test?: CandyTestSummary | null } }>()
const emit = defineEmits<{ (event: 'open'): void }>()
const { t } = useI18n()
const summary = computed(() => ({
  latest: props.account.candy_test?.latest?.prompt_version === PELICAN_TEST_PROMPT_VERSION ? props.account.candy_test.latest : undefined,
  active: props.account.candy_test?.active?.prompt_version === PELICAN_TEST_PROMPT_VERSION ? props.account.candy_test.active : undefined,
}))
const anchor = ref<HTMLElement>()
const box = ref({ top: 0, width: 0, height: 0 })
const layout = inject(pelicanThumbnailLayoutKey, createPelicanThumbnailLayout, true)
const layoutKey = Symbol('pelican-cell')
watch([box, () => props.account.platform, () => summary.value.latest?.status], () => {
  if (props.account.platform === 'openai' && summary.value.latest?.status === 'generated') {
    layout.measure(layoutKey, box.value.width, box.value.height)
  } else {
    layout.remove(layoutKey)
  }
}, { flush: 'sync' })
const visible = ref(false)
const loading = ref(false)
const loadedPreview = ref<{ id: number; html: string }>()
const previewVisible = computed(() => visible.value && box.value.width > 0 && box.value.height > 0)
const html = computed(() => summary.value.latest?.status === 'generated' && loadedPreview.value?.id === summary.value.latest.id ? loadedPreview.value.html : '')
const detailsTitle = computed(() => [
  t('candyTests.title'), props.account.name,
  summary.value.latest ? t(`candyTests.${summary.value.latest.status}`) : t('candyTests.noResult'),
  summary.value.latest?.model, summary.value.latest?.reasoning_effort,
  formatDateTime(summary.value.latest?.finished_at), t('candyTests.details'),
].filter(Boolean).join(' · '))

watch([() => props.account.id, () => props.account.platform, () => summary.value.latest?.id, () => summary.value.latest?.status, previewVisible], async (_, __, onCleanup) => {
  const latest = summary.value.latest
  const controller = new AbortController()
  onCleanup(() => controller.abort())
  loading.value = false
  if (loadedPreview.value?.id !== latest?.id || latest?.status !== 'generated') loadedPreview.value = undefined
  if (props.account.platform !== 'openai' || !latest || latest.status !== 'generated' || !previewVisible.value || loadedPreview.value?.html) return
  loading.value = true
  try {
    const source = latest.html || await loadPelicanThumbnail(props.account.id, latest.id, controller.signal)
    if (!controller.signal.aborted) loadedPreview.value = { id: latest.id, html: source }
  } catch {
    // A failed preview read does not change the persisted test result. The full
    // result dialog remains available through the same account-row button.
  } finally {
    if (!controller.signal.aborted) loading.value = false
  }
})

let resizeObserver: ResizeObserver | undefined
let intersectionObserver: IntersectionObserver | undefined
let container: HTMLElement | undefined
function measure() {
  if (!anchor.value || !container) return
  const style = getComputedStyle(container)
  const paddingTop = parseFloat(style.paddingTop) || 0
  const paddingBottom = parseFloat(style.paddingBottom) || 0
  box.value = {
    top: container.getBoundingClientRect().top + container.clientTop + paddingTop - anchor.value.getBoundingClientRect().top,
    width: anchor.value.clientWidth,
    height: Math.max(0, container.clientHeight - paddingTop - paddingBottom),
  }
}
onMounted(() => {
  if (!anchor.value) return
  container = anchor.value.closest<HTMLElement>('td, [data-field]') || anchor.value.parentElement || undefined
  if (!container) return
  measure()
  resizeObserver = new ResizeObserver(measure)
  resizeObserver.observe(container)
  resizeObserver.observe(anchor.value)
  intersectionObserver = new IntersectionObserver(entries => { visible.value = entries.some(entry => entry.isIntersecting) })
  intersectionObserver.observe(container)
})
onBeforeUnmount(() => { resizeObserver?.disconnect(); intersectionObserver?.disconnect(); layout.remove(layoutKey) })
</script>
