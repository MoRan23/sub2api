<template>
  <div class="pointer-events-none absolute inset-0 overflow-hidden" data-testid="pelican-thumbnail">
    <iframe :srcdoc="srcdoc" :title="t('candyTests.preview')" sandbox="allow-scripts" referrerpolicy="no-referrer" scrolling="no" tabindex="-1" aria-hidden="true" class="absolute border-0 bg-white" :style="frameStyle" />
    <div v-if="$slots.overlay" class="absolute" :style="overlayStyle" data-testid="pelican-preview-overlay">
      <slot name="overlay" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { pelicanPreviewDocument } from './pelicanPreview'
import { PELICAN_THUMBNAIL_WIDTH, PELICAN_THUMBNAIL_HEIGHT } from './pelicanThumbnailLayout'

const props = defineProps<{ html: string; width: number; height: number; maxScale?: number }>()
const { t } = useI18n()
const nonce = document.querySelector<HTMLScriptElement>('script[nonce]')?.nonce || ''
const srcdoc = computed(() => pelicanPreviewDocument(props.html, nonce))
// Render a normal-sized page, then scale the whole viewport uniformly. Resizing
// a row only changes this transform, never srcdoc or the animation lifecycle.
const viewportWidth = PELICAN_THUMBNAIL_WIDTH
const viewportHeight = PELICAN_THUMBNAIL_HEIGHT
const geometry = computed(() => {
  const scale = Math.max(0, Math.min(props.width / viewportWidth, props.height / viewportHeight, props.maxScale ?? 1, 1))
  return { scale, width: viewportWidth * scale, height: viewportHeight * scale, left: (props.width - viewportWidth * scale) / 2, top: (props.height - viewportHeight * scale) / 2 }
})
// The overlay follows the rendered page bounds but never inherits its scale.
const overlayStyle = computed(() => ({
  width: `${geometry.value.width}px`,
  height: `${geometry.value.height}px`,
  left: `${geometry.value.left}px`,
  top: `${geometry.value.top}px`,
}))
const frameStyle = computed(() => {
  const { scale, left, top } = geometry.value
  return {
    width: `${viewportWidth}px`,
    height: `${viewportHeight}px`,
    left: `${left}px`,
    top: `${top}px`,
    transform: `scale(${scale})`,
    transformOrigin: 'top left',
    // Round the actual preview, including when it is inset inside the cell.
    // Compensate for scaling so every corner stays 4px on screen.
    borderRadius: scale > 0 ? `${4 / scale}px` : '0px',
  }
})
</script>
