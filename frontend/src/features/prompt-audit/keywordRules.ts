export function normalizeKeywordRules(values: string[]): string[] {
  const seen = new Set<string>()
  return values.map((value) => value.trim()).filter((value) => {
    if (!value) return false
    const identity = value.startsWith('regex:') ? `regex:${value}` : `literal:${value.toLowerCase()}`
    if (seen.has(identity)) return false
    seen.add(identity)
    return true
  })
}

export function keywordRuleLengthError(values: string[]): string {
  if (values.length > 10000) return 'prompt_audit_too_many_keywords'
  for (const value of values) {
    if (value.startsWith('regex:')) {
      if (Array.from(value).length > 2000) return 'prompt_audit_keyword_regex_too_long'
    } else if (Array.from(value).length > 200) {
      return 'prompt_audit_keyword_too_long'
    }
  }
  // Syntax belongs to the server's Go regexp parser, not JavaScript RegExp.
  return ''
}
