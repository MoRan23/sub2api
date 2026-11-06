import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/vue'
import { defineComponent } from 'vue'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import type { OpsMemoryResponse, OpsMemorySample } from '@/api/admin/ops'
import OpsMemory from '../OpsMemory.vue'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => `${key.split('.').at(-1)}${params ? ` ${Object.values(params).join(' ')}` : ''}` })
}))
vi.mock('vue-chartjs', () => ({
  Line: { props: ['data', 'options'], template: '<div role="img">{{ JSON.stringify(data) }}</div>' }
}))

const SelectStub = defineComponent({
  props: ['modelValue', 'options', 'ariaLabel'],
  emits: ['update:modelValue'],
  template: '<select :aria-label="ariaLabel" :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></select>'
})

const histogram = { bounds: [1, 10, 100], counts: [1, 2, 3, 4] }
function sample(at = new Date().toISOString(), heap = 1024 * 1024): OpsMemorySample {
  return {
    at, rss_bytes: 4 * 1024 * 1024, rss_available: true,
    heap_alloc_bytes: heap, heap_inuse_bytes: heap, heap_idle_bytes: 0, heap_released_bytes: 0,
    heap_objects: 10, stack_inuse_bytes: 1024, sys_bytes: 5 * 1024 * 1024, gc_cycles: 2,
    alloc_bytes_total: heap, free_bytes_total: 0, goroutines: 5, gc_pause_seconds: histogram,
    activity: { active_bodies: 2, active_body_bytes: 10, active_large_bodies: 1, active_large_body_bytes: 8, active_sse: 2, active_sse_buffers: 4, active_sse_buffer_bytes: 128, sse_buffer_max_bytes: 256, body_size_bytes: histogram, sse_buffer_size_bytes: histogram },
    caches: [{ name: 'scheduler_cache_total', entries: 10, estimated_bytes: 1024, max_entries: 100, max_bytes: 2048, hits: 20, misses: 3, evictions: 2, ttl_seconds: 300 }],
    usage_queue: { capacity: 100, waiting: 3, running: 1, submitted: 10, completed: 6, failed: 0, settlement_succeeded: 5, settlement_failed: 1, dropped: 0, sync_fallback: 0, wait_ms: histogram, execution_ms: histogram }
  }
}
function memory(instance = 'node-a', overrides: Partial<OpsMemoryResponse> = {}): OpsMemoryResponse {
  return {
    current_instance_id: 'node-a', selected_instance_id: instance,
    sample_interval_seconds: 10, history_limit: 360, stale_after_seconds: 30, redis_available: true, truncated: false,
    instances: ['node-a', 'node-b'].map(instance_id => ({ instance_id, started_at: new Date().toISOString(), last_seen_at: new Date().toISOString(), stale: false })),
    history: [sample()], profile: { enabled: false, expires_at: null, in_progress: false }, ...overrides
  }
}
function success(data: unknown) { return HttpResponse.json({ code: 0, data }) }
const memoryUrl = '*/api/v1/admin/ops/runtime/memory'
const server = setupServer(http.get(memoryUrl, ({ request }) => success(memory(new URL(request.url).searchParams.get('instance_id') || 'node-a'))))
const mountMemory = () => render(OpsMemory, { global: { stubs: { Select: SelectStub } } })
const enabledProfile = () => ({ enabled: true, expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString(), in_progress: false })

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
beforeEach(() => { vi.clearAllMocks() })
afterEach(() => { cleanup(); server.resetHandlers(); vi.useRealTimers(); vi.restoreAllMocks() })
afterAll(() => server.close())

describe('OpsMemory', () => {
  it('shows per-instance samples, cache counters, fixed histograms and allocation rate', async () => {
    const end = Date.now()
    server.use(http.get(memoryUrl, () => success(memory('node-a', { history: [sample(new Date(end - 10000).toISOString()), sample(new Date(end).toISOString(), 2 * 1024 * 1024)] }))))
    mountMemory()
    await screen.findByText('scheduler_cache_total')
    expect(screen.getByText('fresh')).toBeTruthy()
    expect(screen.getByText('102.4 KB/s')).toBeTruthy()
    expect(screen.getByText('currentNode node-a')).toBeTruthy()
    expect(screen.getByText('gcPause')).toBeTruthy()
    await fireEvent.update(screen.getByRole('combobox', { name: 'chart' }), 'allocationGroup')
    expect(screen.getByRole('img').textContent).toContain('"data":[null,0.1]')
    expect((screen.getByRole('button', { name: 'downloadProfile' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('clears old samples while switching and ignores a late response from the previous node', async () => {
    let releaseOld!: () => void
    let releaseNew!: () => void
    const oldRequest = new Promise<void>(resolve => { releaseOld = resolve })
    const newRequest = new Promise<void>(resolve => { releaseNew = resolve })
    let refreshStarted = false
    let count = 0
    server.use(http.get(memoryUrl, async ({ request }) => {
      const instance = new URL(request.url).searchParams.get('instance_id') || 'node-a'
      if (++count === 1) return success(memory())
      if (instance === 'node-a') { refreshStarted = true; await oldRequest; return success(memory('node-a', { history: [sample(undefined, 99 * 1024 * 1024)] })) }
      await newRequest
      return success(memory('node-b', { history: [sample(undefined, 7 * 1024 * 1024)], profile: enabledProfile() }))
    }))
    mountMemory()
    await screen.findByText('scheduler_cache_total')
    await fireEvent.click(screen.getByRole('button', { name: 'refresh' }))
    await waitFor(() => expect(refreshStarted).toBe(true))
    await fireEvent.update(screen.getByRole('combobox', { name: 'instance' }), 'node-b')
    expect(screen.queryByRole('img')).toBeNull()
    expect(screen.queryByText('scheduler_cache_total')).toBeNull()
    releaseNew()
    await screen.findAllByText('7 MB')
    releaseOld()
    await waitFor(() => expect(screen.getByRole('img').textContent).toContain('"data":[7]'))
    expect(screen.queryByText('99 MB')).toBeNull()
    expect(screen.getByText('localOnly node-a')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'downloadProfile' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'disableProfile' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('pins the selected node across load-balanced responses and rejects mismatched history', async () => {
    const params: Array<string | null> = []
    server.use(http.get(memoryUrl, ({ request }) => {
      params.push(new URL(request.url).searchParams.get('instance_id'))
      return success(params.length === 1 ? memory() : memory('node-b', { current_instance_id: 'node-b' }))
    }))
    mountMemory()
    await screen.findByText('scheduler_cache_total')
    await fireEvent.click(screen.getByRole('button', { name: 'refresh' }))
    await screen.findByText('mismatch')
    expect(params).toEqual([null, 'node-a'])
    expect(screen.queryByRole('img')).toBeNull()
    expect((screen.getByRole('button', { name: 'enableProfile' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('distinguishes stale samples, missing RSS and Redis availability', async () => {
    server.use(http.get(memoryUrl, () => success(memory('node-a', { redis_available: false, truncated: true, history: [{ ...sample(new Date(Date.now() - 40000).toISOString()), rss_available: false }] }))))
    mountMemory()
    await screen.findByText('stale')
    expect(screen.getByText('redisUnavailable')).toBeTruthy()
    expect(screen.getByText('instancesTruncated')).toBeTruthy()
    expect(screen.getByText('unavailable')).toBeTruthy()
    expect(screen.getByRole('img').textContent).toContain('"data":[null]')
  })

  it('shows unavailable for empty history and request errors while retaining instance selection', async () => {
    server.use(http.get(memoryUrl, () => success(memory('node-a', { history: [] }))))
    mountMemory()
    await screen.findByRole('option', { name: 'node-b' })
    await screen.findByText('noSamples')
    expect(screen.getAllByText('unavailable').length).toBeGreaterThan(0)
    server.use(http.get(memoryUrl, () => HttpResponse.json({ message: 'failed' }, { status: 503 })))
    await fireEvent.click(screen.getByRole('button', { name: 'refresh' }))
    await screen.findByText('loadFailed')
    expect(screen.getByRole('option', { name: 'node-b' })).toBeTruthy()
  })

  it('enables only the selected current node and downloads a blob using its explicit identity', async () => {
    const enable = vi.fn()
    const download = vi.fn()
    const createUrl = vi.fn(() => 'blob:heap-test')
    const revokeUrl = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createUrl })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeUrl })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    server.use(
      http.post('*/api/v1/admin/ops/runtime/heap-profile/enable', async ({ request }) => { enable(await request.json()); return success(enabledProfile()) }),
      http.get('*/api/v1/admin/ops/runtime/heap-profile/download', ({ request }) => { download(new URL(request.url).searchParams.get('instance_id')); return new HttpResponse('heap', { headers: { 'Content-Type': 'application/octet-stream' } }) })
    )
    mountMemory()
    await screen.findByText('scheduler_cache_total')
    await fireEvent.click(screen.getByRole('button', { name: 'enableProfile' }))
    await screen.findByRole('button', { name: 'disableProfile' })
    expect(enable).toHaveBeenCalledWith({ instance_id: 'node-a' })
    await fireEvent.click(screen.getByRole('button', { name: 'downloadProfile' }))
    await waitFor(() => expect(click).toHaveBeenCalledOnce())
    expect(download).toHaveBeenCalledWith('node-a')
    expect(createUrl).toHaveBeenCalledOnce()
    expect(revokeUrl).toHaveBeenCalledWith('blob:heap-test')
  })

  it('expires profile controls locally and refreshes history every ten seconds', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    const get = vi.fn()
    server.use(http.get(memoryUrl, () => { get(); return success(memory('node-a', { profile: { ...enabledProfile(), expires_at: new Date(Date.now() + 2000).toISOString() } })) }))
    const { unmount } = mountMemory()
    await screen.findByRole('button', { name: 'disableProfile' })
    await vi.advanceTimersByTimeAsync(3000)
    expect(screen.getByRole('button', { name: 'enableProfile' })).toBeTruthy()
    expect((screen.getByRole('button', { name: 'downloadProfile' }) as HTMLButtonElement).disabled).toBe(true)
    await vi.advanceTimersByTimeAsync(7000)
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    unmount()
    await vi.advanceTimersByTimeAsync(10000)
    expect(get).toHaveBeenCalledTimes(2)
  })
})
