<template>
  <div class="pointer-events-none absolute inset-0 overflow-hidden" data-testid="pelican-thumbnail">
    <iframe :srcdoc="srcdoc" :title="t('candyTests.preview')" sandbox="allow-scripts" referrerpolicy="no-referrer" scrolling="no" tabindex="-1" aria-hidden="true" class="absolute border-0 bg-white" :style="frameStyle" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { pelicanPreviewDocument } from './pelicanPreview'

const props = defineProps<{ html: string; width: number; height: number }>()
const { t } = useI18n()
const nonce = document.querySelector<HTMLScriptElement>('script[nonce]')?.nonce || ''
const srcdoc = computed(() => pelicanPreviewDocument(props.html, nonce))
// Render a normal-sized page, then scale the whole viewport uniformly. Resizing
// a row only changes this transform, never srcdoc or the animation lifecycle.
const viewportWidth = 960
const viewportHeight = 600
const frameStyle = computed(() => {
  const scale = Math.max(0, Math.min(props.width / viewportWidth, props.height / viewportHeight, 1))
  return {
    width: `${viewportWidth}px`,
    height: `${viewportHeight}px`,
    left: `${(props.width - viewportWidth * scale) / 2}px`,
    top: `${(props.height - viewportHeight * scale) / 2}px`,
    transform: `scale(${scale})`,
    transformOrigin: 'top left',
  }
})
</script>
