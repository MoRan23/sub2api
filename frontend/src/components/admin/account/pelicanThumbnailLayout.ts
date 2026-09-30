import { computed, shallowReactive, type InjectionKey } from 'vue'

export const PELICAN_THUMBNAIL_WIDTH = 960
export const PELICAN_THUMBNAIL_HEIGHT = 600
export const PELICAN_THUMBNAIL_GUTTER = 4

// Use the tallest natural row in this account list. Measure other columns so
// preview spacers cannot inflate this value. Never upscale beyond the viewport.
export function createPelicanThumbnailLayout() {
  const sizes = shallowReactive(new Map<symbol, { width: number; height: number }>())
  const scale = computed(() => {
    const rows = [...sizes.values()]
    return Math.min(1, Math.max(0, ...rows.map(row => row.height)) / PELICAN_THUMBNAIL_HEIGHT)
  })
  return {
    scale,
    width: computed(() => scale.value * PELICAN_THUMBNAIL_WIDTH),
    height: computed(() => scale.value * PELICAN_THUMBNAIL_HEIGHT),
    measure(key: symbol, width: number, height: number) {
      if (width > 0 && height > 0) {
        const previous = sizes.get(key)
        if (previous?.width !== width || previous.height !== height) sizes.set(key, { width, height })
      } else {
        sizes.delete(key)
      }
    },
    remove(key: symbol) { sizes.delete(key) },
  }
}

// Cell boxes themselves stretch with the row. Their normal-flow contents keep
// their natural size, allowing the shared preview to shrink again after edits.
export function pelicanRowContentHeight(cells: HTMLElement[]) {
  return Math.max(0, ...cells.map(cell => {
    const style = getComputedStyle(cell)
    const bounds: { top: number; bottom: number }[] = []
    for (const node of cell.childNodes) {
      if (node instanceof Element) {
        const nodeStyle = getComputedStyle(node)
        if (nodeStyle.display === 'none' || ['absolute', 'fixed'].includes(nodeStyle.position)) continue
        const rect = node.getBoundingClientRect()
        bounds.push({ top: rect.top - (parseFloat(nodeStyle.marginTop) || 0), bottom: rect.bottom + (parseFloat(nodeStyle.marginBottom) || 0) })
      } else if (node.nodeType === Node.TEXT_NODE && node.textContent?.trim()) {
        const range = document.createRange()
        range.selectNode(node)
        bounds.push(range.getBoundingClientRect())
      }
    }
    const contentHeight = bounds.length ? Math.max(...bounds.map(bound => bound.bottom)) - Math.min(...bounds.map(bound => bound.top)) : 0
    return contentHeight + (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0)
  }))
}

export const pelicanThumbnailLayoutKey: InjectionKey<ReturnType<typeof createPelicanThumbnailLayout>> = Symbol('pelican-thumbnail-layout')
