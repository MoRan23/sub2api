import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SubscriptionsView from '../SubscriptionsView.vue'

const { list, assign, getAllGroups, showError } = vi.hoisted(() => ({
  list: vi.fn(), assign: vi.fn(), getAllGroups: vi.fn(), showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    subscriptions: { list, assign },
    groups: { getAll: getAllGroups },
    users: { list: vi.fn() }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const permanentGroup = { id: 7, name: 'Permanent quota', platform: 'openai', status: 'active', subscription_type: 'total_quota', total_quota_no_expiry: true }
const finiteGroup = { ...permanentGroup, id: 8, name: 'Finite quota', total_quota_no_expiry: false }
const rows = [
  { id: 1, user_id: 11, group_id: 7, status: 'active', expires_at: null, total_remaining_usd: 12.12345678, user: { email: 'reader@example.com' }, group: permanentGroup },
  { id: 2, user_id: 22, group_id: 8, status: 'expired', expires_at: '2026-01-01T00:00:00Z', total_remaining_usd: 0, group: finiteGroup },
  { id: 3, user_id: 33, group_id: 7, status: 'revoked', group: permanentGroup },
  { id: 4, user_id: 44, group_id: 7, status: 'suspended', group: permanentGroup },
  { id: 5, user_id: 55, group_id: 9, status: 'active', group: { id: 9, subscription_type: 'subscription' } }
]

function mountView() {
  return mount(SubscriptionsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
        DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id" :data-row="row.id"><slot name="cell-actions" :row="row" /></div></div>' },
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        Pagination: true, Select: true, ConfirmDialog: true, Icon: true,
        GroupBadge: true, GroupOptionItem: true, EmptyState: true, Teleport: true, RouterLink: true
      }
    }
  })
}

let wrapper: ReturnType<typeof mountView>
beforeEach(async () => {
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
  localStorage.setItem('auth_user', JSON.stringify({ id: 777 }))
  list.mockResolvedValue({ items: rows, total: rows.length, pages: 1 })
  // The selected row carries its group even when the separate picker directory is empty.
  getAllGroups.mockResolvedValue([])
  assign.mockReset().mockResolvedValue({})
  wrapper = mountView()
  await flushPromises()
})
afterEach(() => {
  wrapper.unmount()
  vi.restoreAllMocks()
})

async function openAppend(id = 1) {
  await wrapper.get(`[data-row="${id}"] [data-test="append-quota"]`).trigger('click')
  return wrapper.get('#assign-subscription-form')
}

describe('subscription row quota append', () => {
  it('only offers appending for active and expired total-quota subscriptions', () => {
    for (const id of [1, 2]) expect(wrapper.find(`[data-row="${id}"] [data-test="append-quota"]`).exists()).toBe(true)
    for (const id of [3, 4, 5]) expect(wrapper.find(`[data-row="${id}"] [data-test="append-quota"]`).exists()).toBe(false)
  })

  it('locks the row target and adds an explicit eight-decimal amount to permanent quota', async () => {
    const form = await openAppend()
    expect(form.get('[data-test="append-quota-user"]').text()).toContain('reader@example.com')
    expect(form.get('[data-test="append-quota-group"]').text()).toContain('Permanent quota')
    expect(form.find('[data-assign-user-search]').exists()).toBe(false)
    expect(form.findComponent({ name: 'Select' }).exists()).toBe(false)
    expect(form.find('input[type="checkbox"]').exists()).toBe(false)
    expect(form.findAll('input[type="number"]')).toHaveLength(1)
    expect(form.get<HTMLInputElement>('#assign-quota-amount').element.value).toBe('')
    expect(form.text()).toContain('subscription.permanent')
    await form.trigger('submit')
    expect(assign).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('subscription.invalidAmount')
    await form.get('#assign-quota-amount').setValue('0.00000001')
    expect(form.get('[data-test="append-quota-preview"]').text()).toContain('subscription.remainingQuota $12.12345678')
    expect(form.get('[data-test="append-quota-preview"]').text()).toContain('subscription.estimatedRemainingQuota $12.12345679')
    await form.trigger('submit')
    await flushPromises()
    expect(assign).toHaveBeenCalledWith({ user_id: 11, group_id: 7, quota_amount: 0.00000001, validity_days: undefined }, expect.any(String))
  })

  it('requires days for a finite expired row without restoring its expired balance', async () => {
    const form = await openAppend(2)
    expect(form.get('[data-test="append-quota-user"]').text()).toContain('#22')
    expect(form.findAll('input[type="number"]')).toHaveLength(2)
    expect(form.text()).not.toContain('subscription.permanent')
    await form.get('#assign-quota-amount').setValue('5')
    const days = form.get('input[type="number"][max="36500"]')
    await days.setValue('0')
    await form.trigger('submit')
    expect(assign).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.subscriptions.validityDaysRequired')
    await days.setValue('7')
    expect(form.get('[data-test="append-quota-preview"]').text()).toContain('subscription.estimatedRemainingQuota $5.00')
    await form.trigger('submit')
    await flushPromises()
    expect(assign).toHaveBeenCalledWith({ user_id: 22, group_id: 8, quota_amount: 5, validity_days: 7 }, expect.any(String))
  })

  it('blocks duplicate sends, keeps uncertain retries stable, and rotates the next successful grant', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    let rejectFirst!: (error: Error) => void
    assign.mockReturnValueOnce(new Promise((_resolve, reject) => { rejectFirst = reject }))
    let form = await openAppend()
    await form.get('#assign-quota-amount').setValue('10')
    await form.trigger('submit')
    await form.trigger('submit')
    expect(assign).toHaveBeenCalledTimes(1)
    const firstKey = assign.mock.calls[0]![1]
    rejectFirst(new Error('Response lost'))
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.cancel')!.trigger('click')
    form = await openAppend()
    await form.get('#assign-quota-amount').setValue('10')
    await form.trigger('submit')
    await flushPromises()
    expect(assign.mock.calls[1]![1]).toBe(firstKey)
    form = await openAppend()
    await form.get('#assign-quota-amount').setValue('10')
    await form.trigger('submit')
    await flushPromises()
    expect(assign).toHaveBeenCalledTimes(3)
    expect(assign.mock.calls[2]![1]).not.toBe(firstKey)
  })

  it('restores the general picker after the row-specific modal closes', async () => {
    await openAppend()
    await wrapper.findAll('button').find(button => button.text() === 'common.cancel')!.trigger('click')
    await wrapper.findAll('button').find(button => button.text() === 'admin.subscriptions.assignSubscription')!.trigger('click')
    const form = wrapper.get('#assign-subscription-form')
    expect(form.find('[data-test="append-quota-user"]').exists()).toBe(false)
    expect(form.find('[data-test="append-quota-group"]').exists()).toBe(false)
    expect(form.find('[data-test="append-quota-preview"]').exists()).toBe(false)
    expect(form.find('[data-assign-user-search]').exists()).toBe(true)
    expect(form.findComponent({ name: 'Select' }).exists()).toBe(true)
    expect(form.find('input[type="checkbox"]').exists()).toBe(true)
  })
})
