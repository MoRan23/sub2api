import { describe, expect, it } from 'vitest'
import { keywordRuleLengthError, normalizeKeywordRules } from '../keywordRules'

describe('keyword rule normalization and limits', () => {
  it('deduplicates literals without folding regex case or merging the two kinds', () => {
    expect(normalizeKeywordRules([' Alpha ', 'alpha', '', 'regex:Alpha', 'regex:alpha', 'REGEX:Alpha', 'regex:Alpha', 'REGEX:alpha'])).toEqual(['Alpha', 'regex:Alpha', 'regex:alpha', 'REGEX:Alpha'])
  })

  it('counts Unicode characters and includes the regex prefix in the 2000 limit', () => {
    expect(keywordRuleLengthError(['字'.repeat(200)])).toBe('')
    expect(keywordRuleLengthError(['字'.repeat(201)])).toBe('prompt_audit_keyword_too_long')
    expect(keywordRuleLengthError(['regex:' + '😀'.repeat(1994)])).toBe('')
    expect(keywordRuleLengthError(['regex:' + '😀'.repeat(1995)])).toBe('prompt_audit_keyword_regex_too_long')
    expect(keywordRuleLengthError(Array(10001).fill('a'))).toBe('prompt_audit_too_many_keywords')
  })

  it('defers Go regex syntax validation to the server', () => {
    expect(keywordRuleLengthError(['regex:(?i)Foo', 'regex:(', 'regex:'])).toBe('')
  })
})
