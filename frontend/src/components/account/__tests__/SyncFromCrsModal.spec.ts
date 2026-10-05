import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
const { preview, sync, showWarning } = vi.hoisted(() => ({ preview: vi.fn(), sync: vi.fn(), showWarning: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { previewFromCrs: preview, syncFromCrs: sync } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showWarning, showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import SyncFromCrsModal from '../SyncFromCrsModal.vue'

describe('CRS import notices', () => {
  it('displays a created account warning and keeps the result open', async () => {
    preview.mockResolvedValue({
      new_accounts: [{ crs_account_id: 'source-1', kind: 'openai', name: 'OAuth import', platform: 'openai', type: 'oauth' }],
      existing_accounts: [],
    })
    sync.mockResolvedValue({
      created: 1, updated: 0, skipped: 0, failed: 0,
      items: [{ crs_account_id: 'source-1', kind: 'openai', name: 'OAuth import', action: 'created', warning: '完成授权后在编辑页验证启用 Daybreak' }],
    })
    const wrapper = mount(SyncFromCrsModal, {
      props: { show: true },
      global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
    })
    await wrapper.get('#crs-base-url').setValue('https://crs.example')
    await wrapper.get('#crs-username').setValue('admin')
    await wrapper.get('#crs-password').setValue('test-password')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="crs-import-warnings"]').text()).toContain('OAuth import — 完成授权后在编辑页验证启用 Daybreak')
    expect(wrapper.emitted('synced')).toHaveLength(1)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(showWarning).toHaveBeenCalledWith('admin.accounts.dataImportCompletedWithWarnings')
    wrapper.unmount()
  })
})
