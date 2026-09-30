<template>
  <section class="space-y-3" data-testid="pelican-preview">
    <div class="flex flex-wrap gap-3 text-sm">
      <button type="button" class="btn btn-secondary" data-testid="pelican-toggle" @click="visible = !visible">{{ t(visible ? 'candyTests.closePreview' : 'candyTests.preview') }}</button>
      <button v-if="visible" type="button" class="btn btn-secondary" data-testid="pelican-enlarge" @click="enlarged = !enlarged">{{ t(enlarged ? 'candyTests.reducePreview' : 'candyTests.enlargePreview') }}</button>
      <button type="button" class="btn btn-secondary" data-testid="pelican-download" @click="download">{{ t('candyTests.downloadHTML') }}</button>
    </div>
    <iframe v-if="visible" :srcdoc="srcdoc" :title="t('candyTests.preview')" sandbox="allow-scripts" referrerpolicy="no-referrer" class="w-full rounded-xl border border-gray-200 bg-white dark:border-dark-600" :class="enlarged ? 'h-[80vh]' : 'h-[480px]'" />
    <details data-testid="pelican-source">
      <summary class="cursor-pointer text-sm font-medium">{{ t('candyTests.source') }}</summary>
      <pre class="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-900">{{ html }}</pre>
    </details>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { pelicanPreviewDocument } from './pelicanPreview'

const props = defineProps<{ html: string; itemId: number }>()
const { t } = useI18n()
const visible = ref(true)
const enlarged = ref(false)
// A polling response may replace the item object, but unchanged source strings
// must not change srcdoc or recreate the iframe (which restarts the animation).
const nonce = document.querySelector<HTMLScriptElement>('script[nonce]')?.nonce || ''
const srcdoc = computed(() => pelicanPreviewDocument(props.html, nonce))
const downloads = new Map<string, ReturnType<typeof setTimeout>>()
function download() {
  const url = URL.createObjectURL(new Blob([props.html], { type: 'text/html;charset=utf-8' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `pelican-${props.itemId}.html`
  anchor.click()
  downloads.set(url, setTimeout(() => { URL.revokeObjectURL(url); downloads.delete(url) }, 1000))
}
onBeforeUnmount(() => {
  for (const [url, timer] of downloads) { clearTimeout(timer); URL.revokeObjectURL(url) }
})
</script>
