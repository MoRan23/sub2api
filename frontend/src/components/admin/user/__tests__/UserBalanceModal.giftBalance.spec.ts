import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

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

const mountModal = (operation: 'add' | 'subtract') => mount(UserBalanceModal, {
  props: { show: true, user, operation },
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

  it('does not expose or send gift balance for subtract operations', async () => {
    const wrapper = mountModal('subtract')

    expect(wrapper.find('[data-test="gift-amount"]').exists()).toBe(false)
    await wrapper.get('[data-test="ordinary-amount"]').setValue('2')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(updateBalance).toHaveBeenCalledWith(42, 2, 'subtract', '', 0)
  })
})
