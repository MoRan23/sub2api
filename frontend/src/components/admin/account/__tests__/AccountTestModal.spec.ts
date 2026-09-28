import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountTestModal from '../AccountTestModal.vue'

const { getAvailableModels, copyToClipboard } = vi.hoisted(() => ({
  getAvailableModels: vi.fn(),
  copyToClipboard: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAvailableModels
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'admin.accounts.imagePromptDefault': 'Generate a cute orange cat astronaut sticker on a clean pastel background.',
    'admin.accounts.testResponseInfo.model': '上游返回模型：{model}',
    'admin.accounts.testResponseInfo.notReturned': '未返回',
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.accounts.imageReceived' && params?.count) {
          return `received-${params.count}`
        }
        if (key === 'admin.accounts.imagePreviewAlt' && params?.index) {
          return `test-image-${params.index}`
        }
        return (messages[key] || key).replace(/\{(\w+)\}/g, (_, name: string) => String(params?.[name] ?? `{${name}}`))
      }
    })
  }
})

function createStreamResponse(lines: string[]) {
  const encoder = new TextEncoder()
  const chunks = lines.map((line) => encoder.encode(line))
  let index = 0

  return {
    ok: true,
    body: {
      getReader: () => ({
        read: vi.fn().mockImplementation(async () => {
          if (index < chunks.length) {
            return { done: false, value: chunks[index++] }
          }
          return { done: true, value: undefined }
        })
      })
    }
  } as Response
}

function mountModal(account: Record<string, unknown> = {
  id: 42,
  name: 'Gemini Image Test',
  platform: 'gemini',
  type: 'apikey',
  status: 'active'
}) {
  return mount(AccountTestModal, {
    props: {
      show: false,
      account: account.platform === 'openai' && !('openai_oauth_os_profiles' in account)
        ? { ...account, openai_oauth_os_profiles: { default_os: 'windows', profiles: { windows: { authorization: { status: 'authorized' } }, macos: { authorization: { status: 'unauthorized' } }, linux: { authorization: { status: 'authorized' } } } } }
        : account
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: { template: '<div class="select-stub"></div>' },
        TextArea: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template: '<textarea class="textarea-stub" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
        },
        Icon: true
      }
    }
  })
}

describe('AccountTestModal', () => {
  beforeEach(() => {
    getAvailableModels.mockClear()
    getAvailableModels.mockResolvedValue([
      { id: 'gemini-2.0-flash', display_name: 'Gemini 2.0 Flash' },
      { id: 'gemini-2.5-flash-image', display_name: 'Gemini 2.5 Flash Image' },
      { id: 'gemini-3.1-flash-image', display_name: 'Gemini 3.1 Flash Image' }
    ])
    copyToClipboard.mockReset()
    Object.defineProperty(globalThis, 'localStorage', {
      value: {
        getItem: vi.fn((key: string) => (key === 'auth_token' ? 'test-token' : null)),
        setItem: vi.fn(),
        removeItem: vi.fn(),
        clear: vi.fn()
      },
      configurable: true
    })
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"gemini-2.5-flash-image"}\n',
        'data: {"type":"image","image_url":"data:image/png;base64,QUJD","mime_type":"image/png"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('does not gate tests on obsolete per-system authorization summaries', async () => {
    const wrapper = mountModal({ id: 42, name: 'Shared authorization', platform: 'openai', type: 'oauth', status: 'active', openai_oauth_os_profiles: { default_os: 'windows', authorization: { status: 'authorized' }, profiles: { windows: { installation_id: 'installed-only', authorization: { status: 'unauthorized' } } } } })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(getAvailableModels).toHaveBeenCalledWith(42, 'windows')
    const start = wrapper.findAll('button').find(button => button.text().includes('admin.accounts.startTest'))!
    expect(start.attributes('disabled')).toBeUndefined()
    await start.trigger('click')
    await flushPromises()
    expect(global.fetch).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows the actual Excel route declared by the test start event', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'gpt-6-astra', display_name: 'Astra' }])
    global.fetch = vi.fn().mockResolvedValue(createStreamResponse([
      'data: {"type":"test_start","model":"gpt-6-astra","upstream_kind":"excel"}\n',
      'data: {"type":"test_complete","success":true}\n',
    ])) as any
    const wrapper = mountModal({ id: 42, name: 'Excel', platform: 'openai', type: 'oauth', status: 'active', extra: { openai_excel_upstream_enabled: true } })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('admin.accounts.startTest'))!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('candyTests.upstreamKinds.excel')
    wrapper.unmount()
  })

  it('uses any selected identity system for model lookup and the SSE test', async () => {
    const wrapper = mountModal({ id: 42, name: 'OpenAI', platform: 'openai', type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('[data-testid="openai-oauth-os-select"]').setValue('linux')
    await flushPromises()
    expect(getAvailableModels).toHaveBeenLastCalledWith(42, 'linux')
    await wrapper.findAll('button').find(button => button.text().includes('admin.accounts.startTest'))!.trigger('click')
    await flushPromises()
    expect(JSON.parse(vi.mocked(global.fetch).mock.calls[0]![1]!.body as string).os).toBe('linux')
    expect(wrapper.get('option[value="macos"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('gemini 图片模型测试会携带提示词并渲染图片预览', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const promptInput = wrapper.find('textarea.textarea-stub')
    expect(promptInput.exists()).toBe(true)
    await promptInput.setValue('draw a tiny orange cat astronaut')

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'gemini-3.1-flash-image',
      prompt: 'draw a tiny orange cat astronaut'
    })

    const preview = wrapper.find('img[alt="test-image-1"]')
    expect(preview.exists()).toBe(true)
    expect(preview.attributes('src')).toBe('data:image/png;base64,QUJD')
  })

  it('grok 账号测试默认选择 Grok 模型', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'grok-4.3', display_name: 'Grok 4.3' },
      { id: 'grok-build-0.1', display_name: 'Grok Build 0.1' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"grok-4.3"}\n',
        'data: {"type":"content","text":"ok"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 13,
      name: 'Grok Account',
      platform: 'grok',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'grok-4.3',
      prompt: '',
      mode: 'text'
    })
  })

  it('OpenAI Compact 探测会携带 compact 测试模式', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 42,
      name: 'OpenAI OAuth',
      platform: 'openai',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    ;(wrapper.vm as any).selectedModelId = 'gpt-5.4'
    ;(wrapper.vm as any).testMode = 'compact'
    await (wrapper.vm as any).startTest()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toMatchObject({
      model_id: 'gpt-5.4',
      prompt: '',
      mode: 'compact'
    })
  })

  async function runOpenAITest(data: Record<string, unknown>, type = 'oauth') {
    getAvailableModels.mockResolvedValue([{ id: 'requested-model', display_name: 'Requested model' }])
    global.fetch = vi.fn().mockResolvedValue(createStreamResponse([
      'data: {"type":"test_start","model":"requested-model"}\n',
      'data: {"type":"content","text":"Hello from upstream"}\n',
      `data: ${JSON.stringify({ type: 'response_info', data })}\n`,
      'data: {"type":"test_complete","success":true}\n'
    ])) as any
    const wrapper = mountModal({ id: 42, name: 'OpenAI test', platform: 'openai', type, status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await (wrapper.vm as any).startTest()
    await flushPromises()
    return wrapper
  }

  it('shows the returned model and ignores legacy cache evidence', async () => {
    const wrapper = await runOpenAITest({
      upstream_model: 'gpt-6-astra-returned',
      codex_turn_state: { length: 292, expected_length: 292, shape: 'target' },
    })
    const text = wrapper.text()
    expect(text).toContain('上游返回模型：gpt-6-astra-returned')
    expect(text).not.toContain('Codex turn-state：')
    expect(text.indexOf('Hello from upstream')).toBeLessThan(text.indexOf('上游返回模型'))
    expect(text.match(/Hello from upstream/g)).toHaveLength(1)
    wrapper.unmount()
  })

  it('does not replace a missing returned model with the requested model', async () => {
    const wrapper = await runOpenAITest({})
    expect(wrapper.text()).toContain('上游返回模型：未返回')
    expect(wrapper.text()).not.toContain('上游返回模型：requested-model')
    wrapper.unmount()
  })

  it('非 Codex 响应只显示返回模型，额外原始字段不展示', async () => {
    const wrapper = await runOpenAITest({ upstream_model: 'upstream-model', token: 'private-token-must-not-render' }, 'apikey')
    expect(wrapper.text()).toContain('上游返回模型：upstream-model')
    expect(wrapper.text()).not.toContain('Codex turn-state：')
    expect(wrapper.text()).not.toContain('private-token-must-not-render')
    wrapper.unmount()
  })
})
