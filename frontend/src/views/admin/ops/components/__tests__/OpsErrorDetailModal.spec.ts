import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsErrorDetailModal from '../OpsErrorDetailModal.vue'

const mocks = vi.hoisted(() => ({
  getRequestErrorDetail: vi.fn(),
  listRequestErrorUpstreamErrors: vi.fn()
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getRequestErrorDetail: mocks.getRequestErrorDetail,
    getUpstreamErrorDetail: vi.fn(),
    listRequestErrorUpstreamErrors: mocks.listRequestErrorUpstreamErrors
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('OpsErrorDetailModal', () => {
  beforeEach(() => {
    mocks.getRequestErrorDetail.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  })

  it('displays header diagnostics separately for each upstream attempt', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 1, created_at: '2026-10-30T00:00:00Z', status_code: 502,
      upstream_errors: JSON.stringify([
        { upstream_request_id: 'attempt-1', header_diagnostics: { imagegen_request_id: 'image-1', identity_error_code: 'invalid_identity' } },
        { upstream_request_id: 'attempt-2', header_diagnostics: { authorization_error: '<b>access denied</b>' } },
        { header_diagnostics: { identity_error_code: { unexpected: true } } },
      ]),
    })
    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true, OpenAIHeaderDiagnosticsDetails: false } },
    })
    await flushPromises()
    const diagnostics = wrapper.get('[data-testid="ops-header-diagnostics"]')
    expect(diagnostics.findAll('[data-testid="openai-header-diagnostics"]')).toHaveLength(2)
    expect(diagnostics.text()).toContain('attempt-1')
    expect(diagnostics.text()).toContain('attempt-2')
    expect(diagnostics.text()).toContain('image-1')
    expect(diagnostics.text()).toContain('invalid_identity')
    expect(diagnostics.text()).toContain('<b>access denied</b>')
    expect(diagnostics.find('b').exists()).toBe(false)
  })

  it.each([undefined, '[]', 'invalid json', '{}', '[null,{"header_diagnostics":null}]'])('handles old or malformed diagnostic records (%s)', async (upstream_errors) => {
    mocks.getRequestErrorDetail.mockResolvedValue({ id: 1, upstream_errors })
    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } },
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="ops-header-diagnostics"]').exists()).toBe(false)
  })

  it('prioritizes upstream root cause and deduplicates diagnostic payloads', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 1,
      created_at: '2026-08-19T00:00:00Z',
      phase: 'request',
      type: 'upstream_error',
      error_owner: 'provider',
      error_source: 'gateway',
      severity: 'P1',
      status_code: 502,
      upstream_status_code: 429,
      platform: 'openai',
      model: 'gpt-5.6',
      resolved: false,
      request_id: 'rid-1',
      message: 'All available accounts exhausted',
      error_body: '{"error":"same"}',
      upstream_error_message: 'provider rate limit exhausted',
      upstream_error_detail: '{"error":"same"}',
      upstream_errors: '[]',
      account_name: 'account',
      group_name: 'group',
      is_business_limited: false
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /></div>' },
          Icon: true
        }
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('provider rate limit exhausted')
    expect(wrapper.text()).toContain('admin.ops.errorDetail.upstreamStatus')
    expect(wrapper.text()).toContain('429')
    expect(wrapper.findAll('pre')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('admin.ops.errorDetail.payloads.upstream_detail')
  })
})
