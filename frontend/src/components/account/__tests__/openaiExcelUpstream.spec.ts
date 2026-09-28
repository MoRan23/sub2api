import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { supportsOpenAIExcelUpstream, usesOpenAIExcelUpstream } from '../openaiExcelUpstream'
import OpenAIExcelUpstreamToggle from '../OpenAIExcelUpstreamToggle.vue'

describe('Excel upstream account configuration', () => {
  const account = { platform: 'openai', type: 'oauth', credentials: {}, extra: { openai_excel_upstream_enabled: true } } as Account

  it('requires regular OAuth and never inherits a Spark parent route', () => {
    expect(usesOpenAIExcelUpstream(account)).toBe(true)
    expect(usesOpenAIExcelUpstream({ ...account, extra: {} })).toBe(false)
    expect(supportsOpenAIExcelUpstream({ ...account, parent_account_id: 5 })).toBe(false)
    expect(supportsOpenAIExcelUpstream({ ...account, type: 'setup-token' })).toBe(false)
    expect(supportsOpenAIExcelUpstream({ ...account, type: 'apikey' })).toBe(false)
    for (const auth_mode of ['personalAccessToken', 'personal_access_token', 'agentIdentity', 'agent_identity']) {
      expect(supportsOpenAIExcelUpstream({ ...account, credentials: { auth_mode } })).toBe(false)
      expect(supportsOpenAIExcelUpstream({ ...account, credentials: { openai_auth_mode: auth_mode } })).toBe(false)
    }
  })

  it('emits explicit enabled and disabled values', async () => {
    const wrapper = mount(OpenAIExcelUpstreamToggle, {
      props: { modelValue: false },
      global: { plugins: [createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: {} })] }
    })
    await wrapper.get('input').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([true])
    await wrapper.setProps({ modelValue: true })
    await wrapper.get('input').setValue(false)
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual([false])
  })
})
