import { describe, expect, it } from 'vitest'
import {
  readOpenAIServiceTierSettings,
  updateOpenAIServiceTierSetting,
  openAIServiceTierSettingsPatch
} from '../openaiServiceTierSettings'

describe('OpenAI service tier settings', () => {
  it('defaults to off and requires literal booleans', () => {
    expect(readOpenAIServiceTierSettings({ force_openai_fast: 'true', free_openai_fast: 1 }))
      .toEqual(readOpenAIServiceTierSettings())
  })

  it('switches force tiers atomically without changing either free option', () => {
    const original = readOpenAIServiceTierSettings({
      force_openai_fast: true, free_openai_fast: true, free_openai_ultrafast: true
    })
    const changed = updateOpenAIServiceTierSetting(original, 'force_openai_ultrafast', true)
    expect(original.force_openai_fast).toBe(true)
    expect(changed).toEqual({
      force_openai_fast: false, force_openai_ultrafast: true,
      free_openai_fast: true, free_openai_ultrafast: true
    })
    expect(openAIServiceTierSettingsPatch(changed, original)).toEqual({
      force_openai_fast: false, force_openai_ultrafast: true
    })
  })

  it('omits unchanged settings and patches free tiers independently', () => {
    const original = readOpenAIServiceTierSettings({ free_openai_fast: true })
    expect(openAIServiceTierSettingsPatch(original, original)).toEqual({})
    const changed = updateOpenAIServiceTierSetting(original, 'free_openai_ultrafast', true)
    expect(openAIServiceTierSettingsPatch(changed, original)).toEqual({ free_openai_ultrafast: true })
  })
})
