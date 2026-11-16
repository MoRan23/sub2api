import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import UsageExportTasks from '../UsageExportTasks.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), cancel: vi.fn(), remove: vi.fn(), download: vi.fn() }))
vi.mock('@/api/admin/usageExports', () => ({ usageExportsAPI: api }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: ref('en') }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { vi.useFakeTimers(); vi.resetAllMocks(); api.list.mockResolvedValue([]) })
afterEach(() => vi.useRealTimers())
const task = (status = 'running') => ({ id: 'task-1', status, rows_written: 2, total_rows: 10, size_bytes: 100, created_at: '2026-11-16T00:00:00Z', expires_at: null })
const render = () => mount(UsageExportTasks, { props: { show: true, refreshKey: 0 }, global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } } })

describe('usage export tasks', () => {
  it('loads persisted tasks, polls only active work and stops at completion', async () => {
    api.list.mockResolvedValueOnce([task()]).mockResolvedValue([task('completed')])
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('usage.exportTasks.status.running')
    await vi.advanceTimersByTimeAsync(3000)
    expect(wrapper.text()).toContain('usage.exportTasks.status.completed')
    await vi.advanceTimersByTimeAsync(9000)
    expect(api.list).toHaveBeenCalledTimes(2)
    await wrapper.findAll('button').find(button => button.text() === 'usage.exportTasks.download')!.trigger('click')
    await flushPromises()
    expect(api.download).toHaveBeenCalledWith('task-1', expect.any(AbortSignal))
  })

  it('bounds polling and leaving aborts reads without cancelling the task', async () => {
    api.list.mockResolvedValue([task()])
    const wrapper = render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(630000)
    expect(api.list).toHaveBeenCalledTimes(201)
    let resolve: (value: unknown) => void = () => {}
    api.list.mockImplementation(() => new Promise(done => { resolve = done }))
    await wrapper.findAll('button')[0].trigger('click')
    const signal = api.list.mock.calls.at(-1)![0] as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    resolve([task('completed')])
    await flushPromises()
    expect(api.cancel).not.toHaveBeenCalled()
  })

  it('cancels and deletes only from explicit actions', async () => {
    api.list.mockResolvedValueOnce([task()]).mockResolvedValue([task('cancelled')])
    const wrapper = render()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.cancel')!.trigger('click')
    await flushPromises()
    expect(api.cancel).toHaveBeenCalledWith('task-1', expect.any(AbortSignal))
    await wrapper.findAll('button').find(button => button.text() === 'common.delete')!.trigger('click')
    await flushPromises()
    expect(api.remove).toHaveBeenCalledWith('task-1', expect.any(AbortSignal))
  })

  it('reports permission failures and reloads persisted tasks when reopened', async () => {
    api.list.mockRejectedValueOnce(new Error('Export permission revoked'))
    const first = render()
    await flushPromises()
    expect(first.get('[role="alert"]').text()).toContain('Export permission revoked')
    first.unmount()
    api.list.mockResolvedValue([task('completed')])
    const second = render()
    await flushPromises()
    expect(second.text()).toContain('usage.exportTasks.status.completed')
    api.download.mockRejectedValueOnce(new Error('Download permission revoked'))
    await second.findAll('button').find(button => button.text() === 'usage.exportTasks.download')!.trigger('click')
    await flushPromises()
    expect(second.get('[role="alert"]').text()).toContain('Download permission revoked')
    expect(api.cancel).not.toHaveBeenCalled()
  })

  it('polls deleting artifacts until removed without offering more actions', async () => {
    api.list.mockResolvedValueOnce([task('deleting')]).mockResolvedValue([])
    const wrapper = render()
    await flushPromises()
    expect(wrapper.text()).toContain('usage.exportTasks.status.deleting')
    expect(wrapper.text()).not.toContain('usage.exportTasks.download')
    expect(wrapper.text()).not.toContain('common.cancel')
    expect(wrapper.text()).not.toContain('common.delete')
    await vi.advanceTimersByTimeAsync(9000)
    expect(api.list).toHaveBeenCalledTimes(2)
    expect(api.remove).not.toHaveBeenCalled()
    expect(api.cancel).not.toHaveBeenCalled()
  })

  it('bounds polling when deletion remains pending', async () => {
    api.list.mockResolvedValue([task('deleting')])
    render()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(630000)
    expect(api.list).toHaveBeenCalledTimes(201)
  })
})
