import { describe, expect, it } from 'vitest'
import {
  formatReasoningEffort,
  formatReasoningEffortMapping,
  reasoningEffortValuesEqual,
} from '@/utils/format'

describe('formatReasoningEffort', () => {
  it('preserves custom scalar spelling and full length', () => {
    expect(formatReasoningEffort('model_specific_effort_with_more_than_twenty_characters')).toBe('model_specific_effort_with_more_than_twenty_characters')
    expect(formatReasoningEffort('18446744073709551615')).toBe('18446744073709551615')
    expect(formatReasoningEffort('disabled')).toBe('disabled')
    expect(formatReasoningEffort('m_ax')).toBe('m_ax')
    expect(formatReasoningEffort('none')).toBe('None')
    expect(formatReasoningEffortMapping('Custom-A', 'custom_a')).toBe('Custom-A → custom_a')
  })
  it('title-cases known effort values', () => {
    expect(formatReasoningEffort('max')).toBe('Max')
    expect(formatReasoningEffort('x-high')).toBe('XHigh')
    expect(formatReasoningEffort(null)).toBe('-')
  })
})

describe('formatReasoningEffortMapping', () => {
  it('shows a single value when requested and forwarded match', () => {
    expect(formatReasoningEffortMapping('max', 'max')).toBe('Max')
    expect(formatReasoningEffortMapping(null, 'high')).toBe('High')
  })

  it('shows requested then forwarded when mapping changed the value', () => {
    expect(formatReasoningEffortMapping('max', 'xhigh')).toBe('Max → XHigh')
    expect(formatReasoningEffortMapping('high', 'medium')).toBe('High → Medium')
  })
})

describe('reasoningEffortValuesEqual', () => {
  it('treats x-high aliases as equal', () => {
    expect(reasoningEffortValuesEqual('x-high', 'xhigh')).toBe(true)
    expect(reasoningEffortValuesEqual('max', 'xhigh')).toBe(false)
  })
})
