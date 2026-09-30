import { computed, shallowReactive, type InjectionKey } from 'vue'

export const PELICAN_THUMBNAIL_WIDTH = 960
export const PELICAN_THUMBNAIL_HEIGHT = 600

// Scope the measurements to one account list. Absolute previews never affect
// row height; every mounted preview uses the smallest available scale.
export function createPelicanThumbnailLayout() {
  const scales = shallowReactive(new Map<symbol, number>())
  return {
    scale: computed(() => Math.min(1, ...scales.values())),
    measure(key: symbol, width: number, height: number) {
      if (width > 0 && height > 0) {
        scales.set(key, Math.min(width / PELICAN_THUMBNAIL_WIDTH, height / PELICAN_THUMBNAIL_HEIGHT))
      } else {
        scales.delete(key)
      }
    },
    remove(key: symbol) { scales.delete(key) },
  }
}

export const pelicanThumbnailLayoutKey: InjectionKey<ReturnType<typeof createPelicanThumbnailLayout>> = Symbol('pelican-thumbnail-layout')
