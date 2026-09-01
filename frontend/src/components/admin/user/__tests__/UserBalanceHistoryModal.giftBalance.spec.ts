import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminUser } from '@/types'
import UserBalanceHistoryModal from '../UserBalanceHistoryModal.vue'

const { getUserBalanceHistory } = vi.hoisted(() => ({
  getUserBalanceHistory: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { getUserBalanceHistory }
  }
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

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

describe('UserBalanceHistoryModal gift balance', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getUserBalanceHistory.mockResolvedValue({
      items: [{
        id: 1,
        code: 'ADMIN-BALANCE-1',
        type: 'admin_balance',
        value: 10,
        gift_ratio: 25,
        gift_value: 2.5,
        status: 'used',
        used_by: 42,
        used_at: '2026-09-01T01:00:00Z',
        created_at: '2026-09-01T01:00:00Z',
        group_id: null,
        validity_days: 0,
        notes: 'promotion'
      }],
      total: 1,
      page: 1,
      page_size: 15,
      pages: 1,
      total_recharged: 10
    })
  })

  it('shows total wallet balance and splits ordinary and gift history values', async () => {
    const wrapper = mount(UserBalanceHistoryModal, {
      props: { show: false, user },
      global: {
        stubs: {
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /></div>'
          },
          Select: true,
          Icon: true
        }
      }
    })

    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(getUserBalanceHistory).toHaveBeenCalledWith(42, 1, 15, undefined)
    expect(wrapper.text()).toContain('$14.00')
    expect(wrapper.text()).toContain('admin.users.ordinaryBalance $10.00')
    expect(wrapper.text()).toContain('admin.users.giftBalance $4.00')
    expect(wrapper.text()).toContain('+$12.50')
    expect(wrapper.text()).toContain('admin.users.ordinaryBalance +$10.00')
    expect(wrapper.text()).toContain('admin.users.giftBalance +$2.50 (25%)')
  })
})
