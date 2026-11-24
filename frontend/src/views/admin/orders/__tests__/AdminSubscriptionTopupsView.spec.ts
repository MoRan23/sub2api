import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AdminSubscriptionTopupsView from '../AdminSubscriptionTopupsView.vue'

const { api, showError, showSuccess } = vi.hoisted(() => ({
  api: {
    listProducts: vi.fn(), createProduct: vi.fn(), updateProduct: vi.fn(), archiveProduct: vi.fn(),
    createSKU: vi.fn(), updateSKU: vi.fn(), archiveSKU: vi.fn(), listCards: vi.fn(),
    importCards: vi.fn(), revealCard: vi.fn(), setCardEnabled: vi.fn(),
  },
  showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/admin/subscriptionTopups', () => ({ adminSubscriptionTopupsAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

const product = {
  id: 1, name: 'Monthly subscription', description: 'Description', instructions: 'Redeem with provider',
  enabled: true, sort_order: 0,
  skus: [{ id: 2, product_id: 1, name: 'One month', price: 19.9, enabled: true, sort_order: 0, available_stock: 3 }],
}
const cardPage = {
  items: [{ id: 10, sku_id: 2, masked_code: 'code…1234', status: 'available', created_at: '2026-11-24T10:00:00Z' }],
  total: 30, page: 1, page_size: 20, counts: { available: 3, reserved: 2, issued: 20, disabled: 5 },
}

function mountView() {
  return mount(AdminSubscriptionTopupsView, {
    global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      BaseDialog: {
        props: ['show'], emits: ['close'],
        template: '<div v-if="show" role="dialog"><button data-test="close" @click="$emit(\'close\')">Close</button><slot /></div>',
      },
      ConfirmDialog: true,
      Pagination: { props: ['page'], emits: ['update:page'], template: '<button data-test="next" @click="$emit(\'update:page\', page + 1)">Next</button>' },
    } },
  })
}
async function clickLabel(wrapper: VueWrapper, label: string) {
  const button = wrapper.findAll('button').find(item => item.text() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}
async function openPool(wrapper: VueWrapper) {
  await flushPromises()
  await clickLabel(wrapper, 'subscriptionTopup.admin.pool')
}

describe('AdminSubscriptionTopupsView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listProducts.mockResolvedValue({ data: [product] })
    api.listCards.mockResolvedValue({ data: cardPage })
    api.importCards.mockResolvedValue({ data: { imported: 2 } })
    api.revealCard.mockResolvedValue({ data: { code: 'secret-code' } })
    api.setCardEnabled.mockResolvedValue({ data: {} })
  })

  it('loads only masked inventory and queries pagination and status on demand', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('¥19.90')
    expect(api.listCards).not.toHaveBeenCalled()
    await openPool(wrapper)
    expect(wrapper.text()).toContain('code…1234')
    expect(wrapper.text()).toContain('subscriptionTopup.admin.reserved: 2')
    expect(api.revealCard).not.toHaveBeenCalled()
    await wrapper.get('[data-test="next"]').trigger('click')
    await flushPromises()
    expect(api.listCards).toHaveBeenLastCalledWith(2, { page: 2, page_size: 20, status: undefined }, expect.any(AbortSignal))
    await wrapper.get('select').setValue('disabled')
    await flushPromises()
    expect(api.listCards).toHaveBeenLastCalledWith(2, { page: 1, page_size: 20, status: 'disabled' }, expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('normalizes imported lines, rejects duplicates and keeps input on server rejection', async () => {
    const wrapper = mountView()
    await openPool(wrapper)
    await wrapper.get('#topup-codes').setValue(' first \r\n\n second ')
    await clickLabel(wrapper, 'subscriptionTopup.admin.import')
    expect(api.importCards).toHaveBeenCalledWith(2, ['first', 'second'])
    expect((wrapper.get('#topup-codes').element as HTMLTextAreaElement).value).toBe('')

    await wrapper.get('#topup-codes').setValue('same\nsame')
    await clickLabel(wrapper, 'subscriptionTopup.admin.import')
    expect(api.importCards).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('subscriptionTopup.admin.duplicateImport')

    api.importCards.mockRejectedValueOnce(new Error('import failed'))
    await wrapper.get('#topup-codes').setValue('keep-on-error')
    await clickLabel(wrapper, 'subscriptionTopup.admin.import')
    expect((wrapper.get('#topup-codes').element as HTMLTextAreaElement).value).toBe('keep-on-error')
    wrapper.unmount()
  })

  it('enforces both line-count and UTF-8 byte limits before import', async () => {
    const wrapper = mountView()
    await openPool(wrapper)
    for (const invalid of ['字'.repeat(171), Array.from({ length: 1001 }, (_, i) => `code-${i}`).join('\n')]) {
      await wrapper.get('#topup-codes').setValue(invalid)
      await clickLabel(wrapper, 'subscriptionTopup.admin.import')
    }
    expect(api.importCards).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('reveals only on request and drops a delayed secret after the pool closes', async () => {
    const wrapper = mountView()
    await openPool(wrapper)
    await clickLabel(wrapper, 'subscriptionTopup.admin.showCode')
    expect(wrapper.text()).toContain('secret-code')
    await clickLabel(wrapper, 'subscriptionTopup.admin.hideCode')
    expect(wrapper.text()).not.toContain('secret-code')

    let resolveReveal!: (value: { data: { code: string } }) => void
    api.revealCard.mockReturnValueOnce(new Promise(resolve => { resolveReveal = resolve }))
    const reveal = wrapper.findAll('button').find(item => item.text() === 'subscriptionTopup.admin.showCode')!
    await reveal.trigger('click')
    await wrapper.get('[data-test="close"]').trigger('click')
    resolveReveal({ data: { code: 'late-secret' } })
    await flushPromises()
    await openPool(wrapper)
    expect(wrapper.text()).not.toContain('late-secret')
    expect(wrapper.text()).toContain('code…1234')
    wrapper.unmount()
  })

  it('saves product and SKU drafts without enabling them implicitly', async () => {
    const wrapper = mountView()
    await flushPromises()
    await clickLabel(wrapper, 'subscriptionTopup.admin.createProduct')
    await wrapper.get('[role="dialog"] input').setValue('New product')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createProduct).toHaveBeenCalledWith({ name: 'New product', description: '', instructions: '', enabled: false })
    await clickLabel(wrapper, 'subscriptionTopup.admin.createSKU')
    await wrapper.get('[role="dialog"] input').setValue('Annual')
    await wrapper.get('[role="dialog"] input[type="number"]').setValue('199.50')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createSKU).toHaveBeenCalledWith(1, { name: 'Annual', price: 199.5, enabled: false })
    wrapper.unmount()
  })

  it('edits external-service products without subscription fields or resetting stored sorting', async () => {
    api.listProducts.mockResolvedValue({ data: [{ ...product, sort_order: 42, skus: [{ ...product.skus[0], sort_order: 17 }] }] })
    const wrapper = mountView()
    await flushPromises()
    await clickLabel(wrapper, 'common.edit')
    expect(wrapper.get('[role="dialog"]').findAll('input')).toHaveLength(2)
    expect(wrapper.get('[role="dialog"]').find('select').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateProduct).toHaveBeenCalledWith(1, {
      name: product.name, description: product.description, instructions: product.instructions, enabled: true,
    })

    const editSKU = wrapper.findAll('button').filter(button => button.text() === 'common.edit')[1]
    await editSKU.trigger('click')
    expect(wrapper.get('[role="dialog"]').findAll('input[type="number"]')).toHaveLength(1)
    expect(wrapper.get('[role="dialog"]').find('select').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateSKU).toHaveBeenCalledWith(2, { name: product.skus[0].name, price: 19.9, enabled: true })
    wrapper.unmount()
  })
})
