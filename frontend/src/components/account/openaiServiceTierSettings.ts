export const OPENAI_SERVICE_TIER_KEYS = [
  'force_openai_fast',
  'force_openai_ultrafast',
  'free_openai_fast',
  'free_openai_ultrafast'
] as const

export type OpenAIServiceTierKey = typeof OPENAI_SERVICE_TIER_KEYS[number]
export type OpenAIServiceTierSettings = Record<OpenAIServiceTierKey, boolean>

export function readOpenAIServiceTierSettings(extra?: Record<string, unknown> | null): OpenAIServiceTierSettings {
  return {
    force_openai_fast: extra?.force_openai_fast === true,
    force_openai_ultrafast: extra?.force_openai_ultrafast === true,
    free_openai_fast: extra?.free_openai_fast === true,
    free_openai_ultrafast: extra?.free_openai_ultrafast === true
  }
}

export function updateOpenAIServiceTierSetting(
  settings: OpenAIServiceTierSettings, key: OpenAIServiceTierKey, enabled: boolean
): OpenAIServiceTierSettings {
  const next = { ...settings, [key]: enabled }
  if (enabled && key === 'force_openai_fast') next.force_openai_ultrafast = false
  if (enabled && key === 'force_openai_ultrafast') next.force_openai_fast = false
  return next
}

// Always submit the force pair together when its selection changes. Other
// fields remain independent so an old modal cannot overwrite newer settings.
export function openAIServiceTierSettingsPatch(
  current: OpenAIServiceTierSettings, initial: OpenAIServiceTierSettings
): Partial<OpenAIServiceTierSettings> {
  const patch: Partial<OpenAIServiceTierSettings> = {}
  if (current.force_openai_fast !== initial.force_openai_fast ||
      current.force_openai_ultrafast !== initial.force_openai_ultrafast) {
    patch.force_openai_fast = current.force_openai_fast
    patch.force_openai_ultrafast = current.force_openai_ultrafast
  }
  for (const key of ['free_openai_fast', 'free_openai_ultrafast'] as const) {
    if (current[key] !== initial[key]) patch[key] = current[key]
  }
  return patch
}
