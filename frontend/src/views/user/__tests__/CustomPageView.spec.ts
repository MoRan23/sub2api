import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import enMisc from '@/i18n/locales/en/misc'
import zhMisc from '@/i18n/locales/zh/misc'
import type { CustomMenuItem } from '@/types'
import CustomPageView from '../CustomPageView.vue'

const testState = vi.hoisted(() => ({
  route: { params: { id: 'store' } },
  locale: 'zh-CN',
  appStore: {
    cachedPublicSettings: { custom_menu_items: [] as CustomMenuItem[] },
    publicSettingsLoaded: true,
    fetchPublicSettings: vi.fn(),
  },
  authStore: {
    isAdmin: false,
    user: { id: 42 },
    token: 'platform-token',
  },
  adminSettingsStore: {
    customMenuItems: [] as CustomMenuItem[],
  },
  routerPush: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => testState.route,
  useRouter: () => ({ push: testState.routerPush }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const zh = await vi.importActual<typeof import('@/i18n/locales/zh/misc')>(
    '@/i18n/locales/zh/misc',
  )
  const en = await vi.importActual<typeof import('@/i18n/locales/en/misc')>(
    '@/i18n/locales/en/misc',
  )

  const readMessage = (messages: Record<string, unknown>, key: string): string => {
    const value = key.split('.').reduce<unknown>((current, segment) => {
      if (!current || typeof current !== 'object') return undefined
      return (current as Record<string, unknown>)[segment]
    }, messages)
    return typeof value === 'string' ? value : key
  }

  return {
    ...actual,
    useI18n: () => ({
      locale: { value: testState.locale },
      t: (key: string) => readMessage(
        (testState.locale === 'en' ? en.default : zh.default) as Record<string, unknown>,
        key,
      ),
    }),
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => testState.appStore,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => testState.authStore,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => testState.adminSettingsStore,
}))

const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>',
})

enableAutoUnmount(afterEach)

function menuItem(overrides: Partial<CustomMenuItem> = {}): CustomMenuItem {
  return {
    id: 'store',
    label: 'CDK Self Recharge',
    icon_svg: '',
    url: 'https://pay.ldxp.cn/shop/XR56YHVH',
    visibility: 'user',
    sort_order: 0,
    ...overrides,
  }
}

function mountView(locale: 'zh-CN' | 'en' = 'zh-CN') {
  testState.locale = locale

  return mount(CustomPageView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Icon: true,
      },
    },
  })
}

beforeEach(() => {
  testState.route.params.id = 'store'
  testState.locale = 'zh-CN'
  testState.appStore.cachedPublicSettings = { custom_menu_items: [] }
  testState.appStore.publicSettingsLoaded = true
  testState.appStore.fetchPublicSettings.mockReset()
  testState.authStore.isAdmin = false
  testState.authStore.user = { id: 42 }
  testState.authStore.token = 'platform-token'
  testState.adminSettingsStore.customMenuItems = []
  testState.routerPush.mockReset()
  document.documentElement.classList.remove('dark')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('CustomPageView purchase mode', () => {
  it('uses the exact configured URL without platform identity parameters', () => {
    const target = 'https://pay.ldxp.cn/shop/XR56YHVH'
    testState.appStore.cachedPublicSettings.custom_menu_items = [
      menuItem({ url: target, purchase_mode: true }),
    ]

    const wrapper = mountView()
    const iframe = wrapper.get('[data-testid="custom-page-iframe"]')
    const openLink = wrapper.get('[data-testid="purchase-open-new-window"]')

    expect(iframe.attributes('src')).toBe(target)
    expect(openLink.attributes('href')).toBe(target)
    for (const key of ['token', 'user_id', 'theme', 'lang', 'ui_mode', 'src_host', 'src_url']) {
      expect(new URL(iframe.attributes('src')).searchParams.has(key)).toBe(false)
    }
  })

  it('shows guidance, secures the fallback link, and navigates to redeem', async () => {
    testState.appStore.cachedPublicSettings.custom_menu_items = [
      menuItem({ purchase_mode: true }),
    ]

    const wrapper = mountView()
    expect(wrapper.get('[data-testid="purchase-guide"]').text()).toContain('付款后去兑换')
    expect(wrapper.get('[data-testid="purchase-guide"]').text()).toContain(
      '这里只负责买码。支付完成后复制兑换码，打开「兑换」页粘贴，余额才会到账。',
    )
    expect(wrapper.get('[data-testid="purchase-usdt-notice"]').text()).toBe(
      '如需使用USDT支付请使用站内充值/订阅处',
    )

    const iframe = wrapper.get('[data-testid="custom-page-iframe"]')
    expect(iframe.attributes('referrerpolicy')).toBe('no-referrer')
    expect(iframe.attributes('allow')).toBe('payment; clipboard-write')
    expect(iframe.attributes('sandbox')).toBeUndefined()

    const openLink = wrapper.get('[data-testid="purchase-open-new-window"]')
    expect(openLink.attributes('target')).toBe('_blank')
    expect(openLink.attributes('rel')).toBe('noopener noreferrer')

    await wrapper.get('[data-testid="purchase-redeem-button"]').trigger('click')
    expect(testState.routerPush).toHaveBeenCalledWith('/redeem')
  })

  it('renders equivalent English purchase guidance', () => {
    testState.appStore.cachedPublicSettings.custom_menu_items = [
      menuItem({ purchase_mode: true }),
    ]

    const wrapper = mountView('en')
    const guide = wrapper.get('[data-testid="purchase-guide"]').text()
    expect(guide).toContain('Redeem after payment')
    expect(guide).toContain('This page only sells codes.')
    expect(wrapper.get('[data-testid="purchase-usdt-notice"]').text()).toBe(
      'For USDT payments, please use Recharge / Subscription in this site.',
    )
    expect(wrapper.get('[data-testid="purchase-redeem-button"]').text()).toContain('Go to Redeem')
    expect(zhMisc.customPage.purchaseGuideTitle).toBe('付款后去兑换')
    expect(enMisc.customPage.purchaseGuideTitle).toBe('Redeem after payment')
  })
})

describe('CustomPageView legacy modes', () => {
  it('keeps the existing embedded URL context behavior for regular pages', () => {
    testState.appStore.cachedPublicSettings.custom_menu_items = [
      menuItem({ url: 'https://docs.example.com/start', purchase_mode: false }),
    ]

    const wrapper = mountView()
    const iframe = wrapper.get('[data-testid="custom-page-iframe"]')
    const url = new URL(iframe.attributes('src'))

    expect(url.origin + url.pathname).toBe('https://docs.example.com/start')
    expect(url.searchParams.get('user_id')).toBe('42')
    expect(url.searchParams.get('token')).toBe('platform-token')
    expect(url.searchParams.get('theme')).toBe('light')
    expect(url.searchParams.get('lang')).toBe('zh-CN')
    expect(url.searchParams.get('ui_mode')).toBe('embedded')
    expect(url.searchParams.get('src_host')).toBe(window.location.origin)
    expect(url.searchParams.get('src_url')).toBe(window.location.href)
    expect(wrapper.find('[data-testid="purchase-guide"]').exists()).toBe(false)
    expect(iframe.attributes('referrerpolicy')).toBeUndefined()
    expect(iframe.attributes('allow')).toBeUndefined()
  })

  it('keeps missing and invalid URL states out of iframe mode', () => {
    const missing = mountView()
    expect(missing.text()).toContain('页面不存在')
    expect(missing.find('[data-testid="custom-page-iframe"]').exists()).toBe(false)
    missing.unmount()

    testState.appStore.cachedPublicSettings.custom_menu_items = [
      menuItem({ url: 'not-a-url', purchase_mode: true }),
    ]
    const invalid = mountView()
    expect(invalid.text()).toContain('页面链接未配置')
    expect(invalid.find('[data-testid="custom-page-iframe"]').exists()).toBe(false)
  })

  it('keeps Markdown pages in Markdown mode', async () => {
    testState.appStore.cachedPublicSettings.custom_menu_items = [
      menuItem({ url: 'md:getting-started', page_slug: 'getting-started' }),
    ]
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      text: vi.fn().mockResolvedValue('# Getting Started'),
    }))

    const wrapper = mountView('en')
    await flushPromises()

    expect(wrapper.get('.markdown-page-content').text()).toContain('Getting Started')
    expect(wrapper.find('[data-testid="custom-page-iframe"]').exists()).toBe(false)
  })
})
