import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'

const { list, create, update, batchCreate, getAllWithCount, showError } = vi.hoisted(() => ({
  list: vi.fn(), create: vi.fn(), update: vi.fn(), batchCreate: vi.fn(), getAllWithCount: vi.fn(), showError: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { list, create, update, batchCreate, getAllWithCount } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))
const mountView = () => shallowMount(ProxiesView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
    DataTable: { props: ['data'], template: '<div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
    Select: {
      props: ['modelValue', 'options'], emits: ['update:modelValue', 'change'],
      template: `<select :value="modelValue" @change="$emit('update:modelValue', $event.target.value); $emit('change', $event.target.value)">
        <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
      </select>`
    }
  } }
})
let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.clearAllMocks()
  list.mockResolvedValue({ items: [{ id: 9, name: 'SS node', protocol: 'ss', host: 'ss.example', port: 8388, username: 'aes-256-gcm', status: 'active' }], total: 1, pages: 1 })
  getAllWithCount.mockResolvedValue([])
  create.mockResolvedValue({})
  update.mockResolvedValue({})
  batchCreate.mockResolvedValue({ created: 1, skipped: 0 })
})
afterEach(() => wrapper?.unmount())
async function open(label: string) {
  wrapper = mountView()
  await flushPromises()
  await wrapper.findAll('button').find(button => button.text() === label)!.trigger('click')
}

describe('Shadowsocks proxy forms', () => {
  it('offers all ciphers and creates SS without trimming its password', async () => {
    await open('admin.proxies.createProxy')
    const form = wrapper.get('#create-proxy-form')
    await form.get('select').setValue('ss')
    expect(form.text()).toContain('admin.proxies.ssMethod')
    expect(form.text()).toContain('2022-blake3-chacha20-poly1305')
    await form.findAll('select')[1].setValue('aes-256-gcm')
    const inputs = form.findAll('input[type="text"]')
    await inputs[0].setValue('SS node')
    await inputs[1].setValue('ss.example')
    await form.get('input[type="password"]').setValue(' secret ')
    await form.trigger('submit')
    await flushPromises()
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ protocol: 'ss', username: 'aes-256-gcm', password: ' secret ' }))
  })

  it('validates SS2022 keys before posting without echoing the secret', async () => {
    await open('common.edit')
    const form = wrapper.get('#edit-proxy-form')
    await form.findAll('select')[1].setValue('2022-blake3-aes-128-gcm')
    await form.get('input[type="password"]').setValue('do-not-echo-me')
    await form.trigger('submit')
    expect(update).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.proxies.ssKeyInvalid')
  })

  it('preserves an untouched stored SS password on unrelated edits', async () => {
    await open('common.edit')
    await wrapper.get('#edit-proxy-form').trigger('submit')
    await flushPromises()
    expect(update.mock.calls[0][1]).not.toHaveProperty('password')
  })

  it('preserves spaces when replacing an SS password', async () => {
    await open('common.edit')
    const form = wrapper.get('#edit-proxy-form')
    await form.get('input[type="password"]').setValue(' new password ')
    await form.trigger('submit')
    await flushPromises()
    expect(update.mock.calls[0][1].password).toBe(' new password ')
  })

  it('imports a named SS URL and rejects plugins instead of discarding the option', async () => {
    await open('admin.proxies.createProxy')
    await wrapper.findAll('button').find(button => button.text() === 'admin.proxies.batchAdd')!.trigger('click')
    await wrapper.get('textarea').setValue('ss://aes-128-gcm:%20password%20@[2001:db8::1]:8388#My%20node\nss://aes-128-gcm:secret@ss.example:8388?plugin=anything')
    expect(wrapper.text()).toContain('admin.proxies.invalidCount')
    await wrapper.findAll('button').find(button => button.text() === 'admin.proxies.importProxies')!.trigger('click')
    await flushPromises()
    expect(batchCreate).toHaveBeenCalledWith([{ protocol: 'ss', host: '2001:db8::1', port: 8388, username: 'aes-128-gcm', password: ' password ', name: 'My node' }])
  })
})
