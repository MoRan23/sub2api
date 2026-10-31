import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import type { AdminUser } from '@/types'
import UserBalanceModal from '../UserBalanceModal.vue'

const { updateBalance, showError, showSuccess } = vi.hoisted(() => ({
  updateBalance: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { updateBalance }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

const BaseDialogStub = {
  props: ['show'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
}

const user = {
  id: 42,
  username: 'balance-user',
  email: 'balance@example.com',
  role: 'user',
  balance: 10,
  gift_balance: 4,
  total_balance: 14,
  concurrency: 1,
  status: 'active',
  allowed_groups: [],
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
  notes: '',
  current_concurrency: 0
} as AdminUser

enableAutoUnmount(afterEach)

const mountModal = (operation: 'add' | 'subtract', overrides: Partial<AdminUser> = {}) => mount(UserBalanceModal, {
  props: { show: true, user: { ...user, ...overrides }, operation },
  global: { stubs: { BaseDialog: BaseDialogStub } }
})

describe('UserBalanceModal gift balance', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    updateBalance.mockResolvedValue(user)
  })

  it('allows a gift-only admin deposit and sends it separately', async () => {
    const wrapper = mountModal('add')

    expect(wrapper.text()).toContain('admin.users.currentBalance: $14.00')
    expect(wrapper.text()).toContain('admin.users.ordinaryBalance $10.00')
    expect(wrapper.text()).toContain('admin.users.giftBalance $4.00')

    await wrapper.get('[data-test="gift-amount"]').setValue('3.5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(updateBalance).toHaveBeenCalledWith(42, 0, 'add', '', 3.5)
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.emitted('success')).toBeTruthy()
  })

  it('starts both deduction amounts at zero and disables submission until an amount is entered', () => {
    const wrapper = mountModal('subtract')

    expect((wrapper.get('[data-test="ordinary-amount"]').element as HTMLInputElement).value).toBe('0')
    expect((wrapper.get('[data-test="gift-amount"]').element as HTMLInputElement).value).toBe('0')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('admin.users.ordinaryWithdrawAmount')
    expect(wrapper.text()).toContain('admin.users.giftWithdrawAmount')
  })

  it.each([
    ['ordinary only', 2, 0, '$8.00', '$4.00', '$12.00'],
    ['gift only', 0, 1.5, '$10.00', '$2.50', '$12.50'],
    ['both wallets', 2, 1.5, '$8.00', '$2.50', '$10.50'],
  ])('previews and submits %s deductions independently', async (_, ordinary, gift, ordinaryPreview, giftPreview, totalPreview) => {
    const wrapper = mountModal('subtract')
    await wrapper.get('[data-test="ordinary-amount"]').setValue(String(ordinary))
    await wrapper.get('[data-test="gift-amount"]').setValue(String(gift))
    await wrapper.get('textarea').setValue('admin refund')

    expect(wrapper.get('[data-test="new-ordinary-balance"]').text()).toBe(ordinaryPreview)
    expect(wrapper.get('[data-test="new-gift-balance"]').text()).toBe(giftPreview)
    expect(wrapper.get('[data-test="new-total-balance"]').text()).toBe(totalPreview)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(updateBalance).toHaveBeenCalledWith(42, ordinary, 'subtract', 'admin refund', gift)
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.emitted('success')).toBeTruthy()
  })

  it('fills each full wallet amount separately without losing eight decimal places', async () => {
    const wrapper = mountModal('subtract', { balance: 1.12345678, gift_balance: 2.87654321, total_balance: 3.99999999 })
    await wrapper.get('[data-test="withdraw-all-ordinary"]').trigger('click')
    expect((wrapper.get('[data-test="gift-amount"]').element as HTMLInputElement).value).toBe('0')
    expect(wrapper.get('[data-test="new-total-balance"]').text()).toBe('$2.87654321')
    await wrapper.get('[data-test="withdraw-all-gift"]').trigger('click')
    expect(wrapper.get('[data-test="new-total-balance"]').text()).toBe('$0.00')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateBalance).toHaveBeenCalledWith(42, 1.12345678, 'subtract', '', 2.87654321)
  })

  it('allows gift-only deductions with ordinary debt and treats a blank ordinary input as zero', async () => {
    const wrapper = mountModal('subtract', { balance: -2, total_balance: 2 })
    await wrapper.get('[data-test="withdraw-all-ordinary"]').trigger('click')
    expect((wrapper.get('[data-test="ordinary-amount"]').element as HTMLInputElement).value).toBe('0')
    await wrapper.get('[data-test="ordinary-amount"]').setValue('')
    await wrapper.get('[data-test="gift-amount"]').setValue('1')
    expect(wrapper.get('[data-test="new-ordinary-balance"]').text()).toBe('$-2.00')
    expect(wrapper.get('[data-test="new-gift-balance"]').text()).toBe('$3.00')
    expect(wrapper.get('[data-test="new-total-balance"]').text()).toBe('$1.00')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateBalance).toHaveBeenCalledWith(42, 0, 'subtract', '', 1)
  })

  it.each([
    [11, 1, 'admin.users.insufficientOrdinaryBalance'],
    [1, 5, 'admin.users.insufficientGiftBalance'],
  ])('rejects the whole deduction when one requested wallet is insufficient', async (ordinary, gift, errorKey) => {
    const wrapper = mountModal('subtract')
    await wrapper.get('[data-test="ordinary-amount"]').setValue(String(ordinary))
    await wrapper.get('[data-test="gift-amount"]').setValue(String(gift))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateBalance).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith(errorKey)
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it.each([
    [0, 0], [-1, 1], [1, -1], [NaN, 1], [1, Infinity], ['invalid', 1],
  ])('rejects invalid or zero amounts (%s, %s)', async (ordinary, gift) => {
    const wrapper = mountModal('subtract')
    const form = (wrapper.vm as unknown as { form: { amount: number | string; giftAmount: number | string } }).form
    form.amount = ordinary
    form.giftAmount = gift
    await nextTick()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="new-total-balance"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(showError).toHaveBeenCalledWith('admin.users.amountRequired')
    expect(updateBalance).not.toHaveBeenCalled()
  })

  it('resets amounts and notes when reopened or switched to another user', async () => {
    const wrapper = mountModal('subtract')
    await wrapper.get('[data-test="ordinary-amount"]').setValue('2')
    await wrapper.get('[data-test="gift-amount"]').setValue('1')
    await wrapper.get('textarea').setValue('previous')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    expect((wrapper.get('[data-test="ordinary-amount"]').element as HTMLInputElement).value).toBe('0')
    expect((wrapper.get('[data-test="gift-amount"]').element as HTMLInputElement).value).toBe('0')
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    await wrapper.get('[data-test="gift-amount"]').setValue('1')
    await wrapper.setProps({ user: { ...user, id: 43 } })
    expect((wrapper.get('[data-test="gift-amount"]').element as HTMLInputElement).value).toBe('0')
  })
})
