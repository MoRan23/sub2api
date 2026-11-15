<template>
  <slot />
  <Teleport to="body">
    <div
      v-if="active"
      :id="tooltipID"
      ref="popup"
      class="overview-floating-tooltip"
      role="tooltip"
      :style="{ left: `${position.x}px`, top: `${position.y}px`, visibility: position.ready ? 'visible' : 'hidden' }"
    >
      <span v-for="(line, index) in active.lines" :key="index" class="tooltip-line" :class="{ 'tooltip-title': index === 0 }">{{ line }}</span>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, provide, ref, shallowRef, useId, watch } from 'vue'
import { overviewTooltipKey } from './overviewTooltip'

const tooltipID = `overview-history-${useId()}`
const popup = ref<HTMLElement>()
const active = shallowRef<{ anchor: HTMLElement; owner: number; lines: string[]; pinned: boolean } | null>(null)
const position = ref({ x: 0, y: 0, ready: false })

function close() {
  active.value?.anchor.removeAttribute('aria-describedby')
  active.value = null
}

provide(overviewTooltipKey, {
  show(event, owner, lines, pinned = false) {
    const anchor = event.currentTarget
    if (!(anchor instanceof HTMLElement)) return
    if (active.value?.anchor === anchor) {
      if (pinned) {
        if (active.value.pinned) close()
        else active.value = { ...active.value, pinned: true }
      }
      return
    }
    close()
    position.value.ready = false
    const current = { anchor, owner, lines: lines(), pinned }
    active.value = current
    anchor.setAttribute('aria-describedby', tooltipID)
    void nextTick(() => {
      if (active.value !== current || !popup.value || !anchor.isConnected) return
      const rect = anchor.getBoundingClientRect()
      const { width, height } = popup.value.getBoundingClientRect()
      const margin = 12
      const x = Math.max(margin, Math.min(rect.left + rect.width / 2 - width / 2, window.innerWidth - width - margin))
      const above = rect.top - height - 8
      const preferredY = above >= margin ? above : rect.bottom + 8
      const y = Math.max(margin, Math.min(preferredY, window.innerHeight - height - margin))
      position.value = { x, y, ready: true }
    })
  },
  hide(owner, anchor) {
    if (active.value?.owner === owner && active.value.anchor === anchor && !active.value.pinned) close()
  },
  dismiss(owner) { if (active.value?.owner === owner) close() },
})

function outside(event: Event) {
  if (event.target instanceof Node && !active.value?.anchor.contains(event.target)) close()
}
function keydown(event: Event) { if (event instanceof KeyboardEvent && event.key === 'Escape') close() }
function visibility() { if (document.hidden) close() }
function listen(enabled: boolean) {
  const method = enabled ? 'addEventListener' : 'removeEventListener'
  document[method]('pointerdown', outside, true)
  document[method]('keydown', keydown)
  document[method]('visibilitychange', visibility)
  window[method]('scroll', close, true)
  window[method]('resize', close)
}
// One active popup/listener set for all cards; no per-cell DOM or listeners on document.
watch(() => active.value !== null, listen, { flush: 'sync' })
onBeforeUnmount(() => { close(); listen(false) })
</script>

<style scoped>
.overview-floating-tooltip {
  position: fixed;
  pointer-events: none;
  z-index: 9999;
  max-width: min(18rem, calc(100vw - 1.5rem));
  max-height: calc(100vh - 1.5rem);
  overflow: hidden;
  overflow-wrap: anywhere;
  border-radius: 0.75rem;
  border: 1px solid rgb(229 231 235);
  background: white;
  padding: 0.5rem 0.625rem;
  box-shadow: 0 18px 40px -12px rgb(0 0 0 / 0.28);
}
.tooltip-line { display: block; font-size: 11px; line-height: 1.45; color: rgb(75 85 99); }
.tooltip-title { margin-bottom: 0.2rem; font-weight: 600; color: rgb(17 24 39); }
:global(.dark) .overview-floating-tooltip { border-color: rgb(55 65 81); background: rgb(17 24 39); }
:global(.dark) .tooltip-line { color: rgb(209 213 219); }
:global(.dark) .tooltip-title { color: rgb(243 244 246); }
</style>
