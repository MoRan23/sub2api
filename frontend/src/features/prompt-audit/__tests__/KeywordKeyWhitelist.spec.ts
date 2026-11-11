import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import KeywordKeyWhitelist from '../components/KeywordKeyWhitelist.vue'
import type { PromptKeywordKeyOption, PromptKeywordWhitelistIdentity, PromptKeywordWhitelistKey } from '../types'

const api = vi.hoisted(() => ({ users: vi.fn(), keys: vi.fn(), identities: vi.fn() }))
vi.mock('../api', () => ({ listKeywordWhitelistUsers: api.users, listKeywordWhitelistKeys: api.keys, getKeywordWhitelistIdentities: api.identities }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => key + (values ? JSON.stringify(values) : '') }) }))
function page<T>(items: T[], total = items.length) {
  return { items, total, page: 1, page_size: 20, pages: Math.ceil(total / 20) }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
async function chooseUser(wrapper: ReturnType<typeof mount>, user = '1') {
  await wrapper.get('[data-test="search"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-test="user"]').setValue(user)
  await flushPromises()
}

describe('keyword key allowlist', () => {
  beforeEach(() => {
    api.users.mockReset().mockResolvedValue(page([{ id: 1, email: 'alice@example.test', username: 'Alice' }, { id: 2, email: 'bob@example.test', username: 'Bob' }]))
    api.keys.mockReset().mockResolvedValue(page([{ id: 11, user_id: 1, name: 'key-one' }, { id: 12, user_id: 1, name: 'key-two' }, { id: 21, user_id: 2, name: 'wrong-owner' }]))
    api.identities.mockReset().mockImplementation(async (keys: PromptKeywordWhitelistKey[]) => keys.map((key) => ({ ...key, user_email: 'alice@example.test', username: 'Alice', key_name: 'key-one', available: true })))
  })

  it('requires one user and one owned key, and saves only their IDs', async () => {
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: [] } })
    expect(api.users).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.promptAudit.keywordWhitelist.description')
    await chooseUser(wrapper)
    expect(wrapper.get('[data-test="user"]').text()).toContain('alice@example.test / Alice')
    expect(api.keys).toHaveBeenCalledWith(1, 1)
    expect(wrapper.get('[data-test="key"]').text()).not.toContain('wrong-owner')
    expect(wrapper.get('[data-test="add-key"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="key"]').setValue('11')
    await wrapper.get('[data-test="add-key"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[[{ user_id: 1, api_key_id: 11 }]]])
    await wrapper.setProps({ modelValue: [{ user_id: 1, api_key_id: 11 }] })
    expect(wrapper.get('[data-test="saved-entry"]').text()).toContain('alice@example.test')
    await wrapper.get('[data-test="key"]').setValue('11')
    expect(wrapper.get('[data-test="add-key"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="key"]').setValue('12')
    expect(wrapper.get('[data-test="add-key"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('ignores an old owner request and resets key selection on user changes', async () => {
    const old = deferred<ReturnType<typeof page<PromptKeywordKeyOption>>>()
    api.keys.mockImplementation((user: number) => user === 1 ? old.promise : Promise.resolve(page([{ id: 21, user_id: 2, name: 'bob-key' }])))
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: [] } })
    await chooseUser(wrapper)
    await wrapper.get('[data-test="user"]').setValue('2')
    await flushPromises()
    await wrapper.get('[data-test="key"]').setValue('21')
    old.resolve(page([{ id: 11, user_id: 1, name: 'stale-alice-key' }]))
    await flushPromises()
    expect(wrapper.get('[data-test="key"]').text()).not.toContain('stale-alice-key')
    await wrapper.get('[data-test="add-key"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual([{ user_id: 2, api_key_id: 21 }])
    await wrapper.get('[data-test="user"]').setValue('1')
    expect(wrapper.get('[data-test="add-key"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('ignores search results after the query changes', async () => {
    const old = deferred<ReturnType<typeof page<{ id: number; email: string }>>>()
    api.users.mockReturnValueOnce(old.promise)
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: [] } })
    await wrapper.get('[data-test="search"]').trigger('click')
    await wrapper.get('[data-test="search-users"]').setValue('bob')
    await wrapper.get('[data-test="search"]').trigger('click')
    await flushPromises()
    old.resolve(page([{ id: 99, email: 'stale@example.test' }]))
    await flushPromises()
    expect(wrapper.get('[data-test="user"]').text()).not.toContain('stale@example.test')
    expect(api.users).toHaveBeenLastCalledWith('bob', 1)
    wrapper.unmount()
  })

  it('provides pagination for both users and their keys', async () => {
    api.users.mockResolvedValue(page([{ id: 1, email: 'alice@example.test' }], 21))
    api.keys.mockResolvedValue(page([{ id: 11, user_id: 1, name: 'key-one' }], 21))
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: [] } })
    await chooseUser(wrapper)
    await wrapper.get('[data-test="key"]').setValue('11')
    await wrapper.get('[data-test="keys-next"]').trigger('click')
    await flushPromises()
    expect(api.keys).toHaveBeenLastCalledWith(1, 2)
    expect(wrapper.get('[data-test="add-key"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="users-next"]').trigger('click')
    await flushPromises()
    expect(api.users).toHaveBeenLastCalledWith('', 2)
    expect(wrapper.find('[data-test="key"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps saved and deleted identities removable without fetching secrets', async () => {
    api.identities.mockResolvedValue([{ user_id: 900, api_key_id: 999, user_email: '', username: '', key_name: '', available: false }])
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: [{ user_id: 900, api_key_id: 999 }] } })
    await flushPromises()
    expect(wrapper.get('[data-test="saved-entry"]').text()).toContain('900')
    expect(wrapper.get('[data-test="saved-entry"]').text()).toContain('999')
    expect(wrapper.get('[data-test="saved-entry"]').text()).toContain('admin.promptAudit.keywordWhitelist.missingIdentity')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(api.users).not.toHaveBeenCalled()
    expect(api.keys).not.toHaveBeenCalled()
    await wrapper.get('[data-test="remove-key"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual([])
    wrapper.unmount()
  })

  it('loads saved emails, usernames, and key names in a single metadata batch', async () => {
    const saved = [{ user_id: 1, api_key_id: 11 }, { user_id: 2, api_key_id: 21 }]
    api.identities.mockResolvedValue([
      { ...saved[0], user_email: 'alice@example.test', username: 'Alice', key_name: 'main-key', available: true },
      { ...saved[1], user_email: 'bob@example.test', username: '', key_name: '', available: true },
    ])
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: saved } })
    expect(wrapper.text()).toContain('admin.promptAudit.keywordWhitelist.metadataLoading')
    await flushPromises()
    expect(api.identities).toHaveBeenCalledTimes(1)
    expect(api.identities).toHaveBeenCalledWith(saved)
    expect(wrapper.get('[data-test="saved-entry"]').text()).toContain('alice@example.test / Alice · main-key')
    expect(wrapper.text()).toContain('bob@example.test')
    expect(wrapper.text()).toContain('admin.promptAudit.keywordWhitelist.unnamedKey')
    await wrapper.setProps({ modelValue: saved.map((key) => ({ ...key })) })
    expect(api.identities).toHaveBeenCalledTimes(1)
    expect(api.users).not.toHaveBeenCalled()
    expect(api.keys).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('distinguishes a failed lookup from a deleted key and allows retry and removal', async () => {
    api.identities.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: [{ user_id: 1, api_key_id: 11 }] } })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.promptAudit.keywordWhitelist.metadataLoadFailed')
    expect(wrapper.text()).not.toContain('admin.promptAudit.keywordWhitelist.missingIdentity')
    expect(wrapper.get('[data-test="remove-key"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-test="retry-identities"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('alice@example.test / Alice · key-one')
    expect(wrapper.find('[data-test="retry-identities"]').exists()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })

  it('ignores stale identity batches after the saved pairs change or clear', async () => {
    const old = deferred<PromptKeywordWhitelistIdentity[]>()
    api.identities.mockReturnValueOnce(old.promise).mockResolvedValueOnce([
      { user_id: 2, api_key_id: 21, user_email: 'bob@example.test', username: 'Bob', key_name: 'bob-key', available: true },
    ])
    const wrapper = mount(KeywordKeyWhitelist, { props: { modelValue: [{ user_id: 1, api_key_id: 11 }] } })
    await wrapper.setProps({ modelValue: [{ user_id: 2, api_key_id: 21 }] })
    await flushPromises()
    old.resolve([{ user_id: 1, api_key_id: 11, user_email: 'stale@example.test', username: '', key_name: 'stale-key', available: true }])
    await flushPromises()
    expect(wrapper.text()).toContain('bob@example.test / Bob · bob-key')
    expect(wrapper.text()).not.toContain('stale')

    const pending = deferred<PromptKeywordWhitelistIdentity[]>()
    api.identities.mockReturnValueOnce(pending.promise)
    await wrapper.setProps({ modelValue: [{ user_id: 3, api_key_id: 31 }] })
    await wrapper.setProps({ modelValue: [] })
    pending.resolve([{ user_id: 3, api_key_id: 31, user_email: 'cleared@example.test', username: '', key_name: 'cleared-key', available: true }])
    await flushPromises()
    expect(wrapper.text()).not.toContain('cleared')
    expect(wrapper.find('[data-test="saved-entry"]').exists()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })
})
