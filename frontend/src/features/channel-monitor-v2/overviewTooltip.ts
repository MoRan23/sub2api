import type { InjectionKey } from 'vue'

export interface OverviewTooltipController {
  show(event: MouseEvent | FocusEvent, owner: number, lines: () => string[], pinned?: boolean): void
  hide(owner: number, anchor?: EventTarget | null): void
  dismiss(owner: number): void
}

export const overviewTooltipKey: InjectionKey<OverviewTooltipController> = Symbol('overview-history-tooltip')
