import { describe, expect, it } from 'vitest'
import { computed, defineComponent, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import KeywordEditor from '../components/KeywordEditor.vue'

function mountEditor(initialKeywords: string[] = ['first']) {
  const keywords = ref(initialKeywords)
  const Parent = defineComponent({
    components: { KeywordEditor },
    setup() {
      // The settings parent replaces its draft and sends a new array back.
      const model = computed({
        get: () => keywords.value,
        set: (value: string[]) => { keywords.value = [...value] },
      })
      return { model }
    },
    template: '<KeywordEditor v-model="model" label="Keywords" :count-label="`${model.length} configured`" limit-label="limits" />',
  })
  const wrapper = mount(Parent)
  const textarea = wrapper.get<HTMLTextAreaElement>('textarea')
  return { wrapper, textarea, keywords }
}

describe('KeywordEditor editing draft', () => {
  it('keeps consecutive newlines and lets a new keyword be entered after Enter', async () => {
    const { textarea, keywords } = mountEditor()

    await textarea.setValue('first\n')
    expect(textarea.element.value).toBe('first\n')
    expect(keywords.value).toEqual(['first'])

    await textarea.setValue('first\n\n')
    expect(textarea.element.value).toBe('first\n\n')
    await textarea.setValue('first\n\nsecond')
    expect(textarea.element.value).toBe('first\n\nsecond')
    expect(keywords.value).toEqual(['first', 'second'])
  })

  it('normalizes the saved list while preserving whitespace and duplicate draft lines', async () => {
    const { textarea, keywords } = mountEditor()
    const draft = ' Alpha \n\nalpha\n 中文 关键词 \n'

    await textarea.setValue(draft)
    expect(keywords.value).toEqual(['Alpha', '中文 关键词'])
    expect(textarea.element.value).toBe(draft)

    keywords.value = [...keywords.value]
    await nextTick()
    expect(textarea.element.value).toBe(draft)
  })

  it('syncs externally loaded, replaced, and cleared keyword lists', async () => {
    const { textarea, keywords } = mountEditor([])

    keywords.value = ['loaded', '配置']
    await nextTick()
    expect(textarea.element.value).toBe('loaded\n配置')
    await textarea.setValue('loaded\n配置\n')

    keywords.value = ['replacement']
    await nextTick()
    expect(textarea.element.value).toBe('replacement')

    keywords.value.push('added externally')
    await nextTick()
    expect(textarea.element.value).toBe('replacement\nadded externally')

    keywords.value = []
    await nextTick()
    expect(textarea.element.value).toBe('')
  })

  it('waits for composition to finish and preserves the newline before Chinese input', async () => {
    const { textarea, keywords } = mountEditor()
    await textarea.setValue('first\n')
    await textarea.trigger('compositionstart')
    textarea.element.value = 'first\nzhong'
    await textarea.trigger('input')
    expect(keywords.value).toEqual(['first'])

    textarea.element.value = 'first\n中文'
    await textarea.trigger('compositionend')
    expect(keywords.value).toEqual(['first', '中文'])
    expect(textarea.element.value).toBe('first\n中文')
  })
})
