import { beforeEach, describe, expect, it } from 'vitest'
import { completeSubscriptionGrant, prepareSubscriptionGrant } from '../subscriptionGrantOperation'

describe('subscription grant operation keys', () => {
  beforeEach(() => {
    sessionStorage.clear()
    localStorage.setItem('auth_user', JSON.stringify({ id: 777 }))
  })

  it('reuses the same key for uncertain retries, including canonical batch order', () => {
    const first = prepareSubscriptionGrant({ user_ids: [22, 11], group_id: 3, quota_amount: 10 })
    const retry = prepareSubscriptionGrant({ user_ids: [11, 22], group_id: 3, quota_amount: 10 })
    expect(retry.key).toBe(first.key)
    expect(retry.request.user_ids).toEqual([11, 22])
    completeSubscriptionGrant(first)
  })

  it('rotates only after confirmed success and isolates modified grants and administrators', () => {
    const request = { user_id: 11, group_id: 3, quota_amount: 10 }
    const first = prepareSubscriptionGrant(request)
    expect(prepareSubscriptionGrant({ ...request, quota_amount: 20 }).key).not.toBe(first.key)
    localStorage.setItem('auth_user', JSON.stringify({ id: 778 }))
    expect(prepareSubscriptionGrant(request).key).not.toBe(first.key)
    localStorage.setItem('auth_user', JSON.stringify({ id: 777 }))
    completeSubscriptionGrant(first)
    expect(prepareSubscriptionGrant(request).key).not.toBe(first.key)
  })
})
