import { describe, expect, it } from 'vitest'
import { validAttributionExpectedModels, validAttributionModel } from '../modelAttribution'

describe('attribution model IDs', () => {
  it.each(['5.6 Cyber', '5.6  Cyber', '  5.6 Cyber  ', 'x'.repeat(200)])('accepts ordinary spaces and the length boundary: %j', model => {
    expect(validAttributionModel(model)).toBe(true)
  })

  it.each(['', '   ', '5.6*', '5.6\tCyber', '5.6\nCyber', '5.6\rCyber', '5.6\u0000Cyber', '5.6\u007fCyber', '5.6\u0085Cyber', '5.6\u00a0Cyber', '5.6\u2003Cyber', 'x'.repeat(201)])('rejects an invalid probe or tier ID: %j', model => {
    expect(validAttributionModel(model)).toBe(false)
  })

  it('keeps detector expectation IDs free of internal whitespace', () => {
    expect(validAttributionExpectedModels(['5.6 Cyber'])).toBe(false)
    expect(validAttributionExpectedModels(['gpt-6-astra'])).toBe(true)
  })
})
