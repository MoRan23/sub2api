import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import type { CustomMenuItem } from '@/types'
import AppSidebar from '../AppSidebar.vue'

const state = vi.hoisted(() => ({
  app: {
    sidebarCollapsed: false,
    mobileOpen: false,
    sidebarScrollTop: 0,
    publicSettingsLoaded: true,
    backendModeEnabled: false,
    siteName: 'Test',
    siteLogo: '',
    siteVersion: '',
    cachedPublicSettings: { custom_menu_items: [] as CustomMenuItem[] },
  },
  auth: { isAdmin: false, isSimpleMode: false },
  admin: { customMenuItems: [] as CustomMenuItem[], fetch: vi.fn() },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => state.app,
  useAuthStore: () => state.auth,
  useAdminSettingsStore: () => state.admin,
  useOnboardingStore: () => ({ isCurrentStep: () => false }),
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => state.app }))
vi.mock('@/composables/useBatchImageAccess', () => ({
  useBatchImageAccess: () => ({
    canUseBatchImage: { value: false },
    refreshBatchImageAccess: vi.fn(),
  }),
}))
vi.mock('vue-router', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-router')>(),
  useRoute: () => ({ path: '/custom/store' }),
  useRouter: () => ({ push: vi.fn() }),
}))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

enableAutoUnmount(afterEach)

beforeEach(() => {
  state.app.cachedPublicSettings.custom_menu_items = []
  state.admin.customMenuItems = []
  state.auth.isAdmin = false
  state.auth.isSimpleMode = false
})

function renderMenu(item: CustomMenuItem) {
  if (item.visibility === 'admin') {
    state.admin.customMenuItems = [item]
  } else {
    state.app.cachedPublicSettings.custom_menu_items = [item]
  }
  return mount(AppSidebar, {
    global: {
      stubs: {
        RouterLink: defineComponent({
          props: ['to'],
          template: '<a :href="to"><slot /></a>',
        }),
        VersionBadge: true,
        Icon: true,
      },
    },
  }).get('a[href="/custom/store"]')
}

describe.each([
  { name: 'user menu', isAdmin: false, isSimpleMode: false, visibility: 'user' as const },
  { name: 'admin personal menu', isAdmin: true, isSimpleMode: false, visibility: 'user' as const },
  { name: 'admin menu', isAdmin: true, isSimpleMode: false, visibility: 'admin' as const },
  { name: 'simple admin menu', isAdmin: true, isSimpleMode: true, visibility: 'admin' as const },
])('AppSidebar custom icons in $name', ({ isAdmin, isSimpleMode, visibility }) => {
  function menu(icon_svg: string, purchase_mode = false): CustomMenuItem {
    Object.assign(state.auth, { isAdmin, isSimpleMode })
    return {
      id: 'store', label: 'Store', url: 'https://example.com/store',
      sort_order: 0, visibility, icon_svg, purchase_mode,
    }
  }

  it.each([
    ['', false],
    ['  ', true],
    ['<script>alert(1)</script>', true],
  ] as const)('shows a fallback for empty or removed SVG content %j', (svg, purchaseMode) => {
    const link = renderMenu(menu(svg, purchaseMode))
    expect(link.get('svg').classes()).toContain('h-5')
    expect(link.get('svg path').attributes('d')).toBeTruthy()
    expect(link.find('.sidebar-svg-icon').exists()).toBe(false)
  })

  it('keeps the configured SVG and its colors while removing unsafe attributes', () => {
    const link = renderMenu(menu('<svg viewBox="0 0 24 24" fill="#123456" onclick="alert(1)"><circle cx="12" cy="12" r="9" /></svg>', true))
    expect(link.get('.sidebar-svg-icon svg').attributes('fill')).toBe('#123456')
    expect(link.get('.sidebar-svg-icon circle').attributes('r')).toBe('9')
    expect(link.get('svg').attributes('onclick')).toBeUndefined()
    expect(link.findAll('svg')).toHaveLength(1)
  })
})
