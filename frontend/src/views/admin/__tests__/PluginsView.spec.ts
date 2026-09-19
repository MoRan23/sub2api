import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PluginsView from '../PluginsView.vue'

const {
  listPlugins,
  uploadPlugin,
  enablePlugin,
  savePluginConfig,
  createUISession,
  stepUpRun,
  pluginStatus,
  showError,
  showSuccess,
  showInfo,
} = vi.hoisted(() => ({
  listPlugins: vi.fn(),
  uploadPlugin: vi.fn(),
  enablePlugin: vi.fn(),
  savePluginConfig: vi.fn(),
  createUISession: vi.fn(),
  stepUpRun: vi.fn((action: () => Promise<unknown>) => action()),
  pluginStatus: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showInfo: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    plugins: {
      list: listPlugins,
      upload: uploadPlugin,
      enable: enablePlugin,
      disable: vi.fn(),
      remove: vi.fn(),
      getConfig: vi.fn().mockResolvedValue({}),
      saveConfig: savePluginConfig,
      test: vi.fn().mockResolvedValue({ success: true, message: 'ok', latency_ms: 1 }),
      createUISession,
      status: pluginStatus,
    },
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo,
  }),
}))

vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: stepUpRun }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => '',
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

const plugin = {
  id: 7,
  plugin_key: 'local.test.transport',
  name: 'Test Transport',
  version: '1.0.0',
  description: '',
  author: 'test',
  manifest: {
    schema_version: 1,
    id: 'local.test.transport',
    name: 'Test Transport',
    version: '1.0.0',
    requires: {
      sub2api: '>=0.1.0',
      plugin_protocol: 1,
      transport_api: 1,
      ui_bridge: 1,
    },
    capabilities: [],
    ui: { entrypoint: 'ui/index.html' },
  },
  binary_sha256: 'a'.repeat(64),
  signature_status: 'trusted' as const,
  state: 'disabled' as const,
  last_error: '',
  installed_at: '2026-08-22T00:00:00Z',
  updated_at: '2026-08-22T00:00:00Z',
  bindings: [
    {
      id: 1,
      plugin_id: 7,
      capability: 'openai.oauth.outbound_transport.v1',
      platform: 'openai',
      account_type: 'oauth',
      enabled: false,
      rollout_percent: 100,
    },
  ],
  compatibility: {
    compatible: true,
    tested: true,
    status: 'compatible' as const,
    message: '',
    current_sub2api_version: '0.1.0',
    required_sub2api_version: '>=0.1.0',
    recommended_sub2api_version: '0.1.0',
    plugin_protocol: 1,
    transport_api: 1,
    ui_bridge: 1,
  },
  runtime_healthy: false,
  runtime_message: '',
}

const mounted: VueWrapper[] = []

function mountView() {
  const wrapper = mount(PluginsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: { template: '<div><slot /></div>' },
        Icon: true,
        TotpStepUpDialog: true,
      },
    },
  })
  mounted.push(wrapper)
  return wrapper
}

describe('管理员插件页二次验证', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    stepUpRun.mockImplementation((action: () => Promise<unknown>) => action())
    listPlugins.mockResolvedValue([plugin])
    uploadPlugin.mockResolvedValue(plugin)
    enablePlugin.mockResolvedValue(plugin)
    savePluginConfig.mockResolvedValue({ enabled: true })
    pluginStatus.mockReset().mockResolvedValue({ healthy: true, message: 'running', status_json: '{"active":2}' })
    createUISession.mockResolvedValue({
      url: '/api/v1/plugin-ui/token/index.html#bridge_token=bridge',
      bridge_token: 'bridge',
      ui_bridge_version: 1,
      expires_at: '2026-08-22T01:00:00Z',
    })
  })

  afterEach(() => {
    mounted.splice(0).forEach(wrapper => wrapper.unmount())
    vi.restoreAllMocks()
  })

  async function openPluginFrame() {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('admin.plugins.configure'))!.trigger('click')
    await flushPromises()
    const frame = wrapper.get<HTMLIFrameElement>('iframe')
    await frame.trigger('load')
    const source = frame.element.contentWindow!
    const postMessage = vi.spyOn(source, 'postMessage').mockImplementation(() => {})
    return { wrapper, frame, source, postMessage }
  }

  function sendStatus(source: Window, data: Record<string, unknown> = {}, origin = 'null') {
    window.dispatchEvent(new MessageEvent('message', {
      source, origin,
      data: { source: 'sub2api-plugin-ui', bridge_token: 'bridge', type: 'plugin.status', request_id: 'status-1', ...data },
    }))
  }

  it.each([
    { healthy: true, message: 'running', status_json: '{"active":2}' },
    { healthy: false, message: 'plugin is not running' },
  ])('returns read-only runtime snapshot without step-up or host toast: $message', async (result) => {
    pluginStatus.mockResolvedValue(result)
    const { source, postMessage } = await openPluginFrame()
    sendStatus(source)
    await flushPromises()
    expect(pluginStatus).toHaveBeenCalledWith(7)
    expect(postMessage).toHaveBeenCalledWith({
      source: 'sub2api-plugin-host', bridge_token: 'bridge', type: 'plugin.status.result', request_id: 'status-1', ok: true, result,
    }, '*')
    expect(stepUpRun).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showInfo).not.toHaveBeenCalled()
  })

  it('returns status API errors only to the requesting plugin without step-up', async () => {
    pluginStatus.mockRejectedValue(new Error('runtime unavailable'))
    const { source, postMessage } = await openPluginFrame()
    sendStatus(source)
    await flushPromises()
    expect(postMessage).toHaveBeenCalledWith(expect.objectContaining({ ok: false, request_id: 'status-1', error: 'runtime unavailable' }), '*')
    expect(stepUpRun).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it('rejects status messages from another source, origin, token, or missing request ID', async () => {
    const { source, postMessage } = await openPluginFrame()
    sendStatus(window)
    sendStatus(source, {}, 'https://untrusted.example')
    sendStatus(source, { source: 'other-ui' })
    sendStatus(source, { bridge_token: 'wrong' })
    sendStatus(source, { request_id: '' })
    sendStatus(source, { request_id: 1 })
    await flushPromises()
    expect(pluginStatus).not.toHaveBeenCalled()
    expect(postMessage).not.toHaveBeenCalled()
  })

  it('deduplicates pending request IDs', async () => {
    let resolve!: (value: unknown) => void
    pluginStatus.mockImplementation(() => new Promise(done => { resolve = done }))
    const { source, postMessage } = await openPluginFrame()
    sendStatus(source)
    sendStatus(source)
    expect(pluginStatus).toHaveBeenCalledTimes(1)
    resolve({ healthy: true, message: 'ready' })
    await flushPromises()
    expect(postMessage).toHaveBeenCalledTimes(1)
  })

  it('does not deliver a stale status result after iframe navigation reuses a request ID', async () => {
    let resolveOld!: (value: unknown) => void
    let resolveNew!: (value: unknown) => void
    pluginStatus.mockImplementationOnce(() => new Promise(done => { resolveOld = done }))
      .mockImplementationOnce(() => new Promise(done => { resolveNew = done }))
    const { frame, source, postMessage } = await openPluginFrame()
    sendStatus(source)
    await frame.trigger('load')
    sendStatus(source)
    resolveOld({ healthy: true, message: 'stale' })
    await flushPromises()
    expect(postMessage).not.toHaveBeenCalled()
    resolveNew({ healthy: false, message: 'current' })
    await flushPromises()
    expect(postMessage).toHaveBeenCalledTimes(1)
    expect(postMessage).toHaveBeenCalledWith(expect.objectContaining({ result: { healthy: false, message: 'current' } }), '*')
  })

  it('drops a status response after its pending request expires', async () => {
    let resolve!: (value: unknown) => void
    pluginStatus.mockImplementation(() => new Promise(done => { resolve = done }))
    const { source, postMessage } = await openPluginFrame()
    vi.useFakeTimers()
    try {
      sendStatus(source)
      await vi.advanceTimersByTimeAsync(30_001)
      resolve({ healthy: true, message: 'late result' })
      await flushPromises()
      expect(postMessage).not.toHaveBeenCalled()
      expect(stepUpRun).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not deliver an old status response after switching plugin sessions', async () => {
    let resolveOld!: (value: unknown) => void
    let resolveNew!: (value: unknown) => void
    pluginStatus.mockImplementationOnce(() => new Promise(done => { resolveOld = done }))
      .mockImplementationOnce(() => new Promise(done => { resolveNew = done }))
    const { wrapper, source, postMessage: oldPostMessage } = await openPluginFrame()
    sendStatus(source)
    createUISession.mockResolvedValueOnce({
      url: '/api/v1/plugin-ui/next/index.html#bridge_token=next-bridge',
      bridge_token: 'next-bridge', ui_bridge_version: 1, expires_at: '2026-08-22T02:00:00Z',
    })
    await wrapper.findAll('button').find(button => button.text().includes('admin.plugins.configure'))!.trigger('click')
    await flushPromises()
    const frame = wrapper.get<HTMLIFrameElement>('iframe')
    await frame.trigger('load')
    const newSource = frame.element.contentWindow!
    const newPostMessage = newSource === source ? oldPostMessage : vi.spyOn(newSource, 'postMessage').mockImplementation(() => {})
    sendStatus(newSource, { bridge_token: 'next-bridge' })
    resolveOld({ healthy: true, message: 'old session' })
    await flushPromises()
    expect(newPostMessage).not.toHaveBeenCalled()
    resolveNew({ healthy: true, message: 'new session' })
    await flushPromises()
    expect(newPostMessage).toHaveBeenCalledWith(expect.objectContaining({ bridge_token: 'next-bridge', result: { healthy: true, message: 'new session' } }), '*')
  })

  it('启用插件通过 step-up 控制器执行', async () => {
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.findAll('button').find((item) => item.text().includes('admin.plugins.enable'))
    expect(button).toBeDefined()
    await button!.trigger('click')
    await flushPromises()

    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(enablePlugin).toHaveBeenCalledWith(7, 100, false)
  })

  it('上传插件通过 step-up 控制器执行', async () => {
    const wrapper = mountView()
    await flushPromises()
    const input = wrapper.get('input[type="file"]')
    Object.defineProperty(input.element, 'files', {
      configurable: true,
      value: [new File(['plugin'], 'transport.s2plugin', { type: 'application/zip' })],
    })

    await input.trigger('change')
    await flushPromises()

    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(uploadPlugin).toHaveBeenCalledTimes(1)
  })
})
