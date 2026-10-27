import type { AttributionPolicy, AttributionStatus } from '@/api/admin/modelAttribution'
import { isValidWildcardPattern, type ModelMappingEntry } from '@/composables/useModelWhitelist'

export function isAttributionActive(status: AttributionStatus): boolean {
  return status === 'queued' || status === 'running' || status === 'retry_wait'
}

export function validAttributionExpectedModels(models?: string[] | null): boolean {
  return models == null || models.length <= 500 && models.every(model => {
    const id = model.trim()
    return id.length > 0 && id.length <= 200 && !/[\s*]/.test(id)
  })
}

export function attributionMappingError(rows: ModelMappingEntry[]): string | undefined {
  if (rows.length > 500) return 'tooManyMappings'
  const seen = new Set<string>()
  for (const row of rows) {
    const from = row.from.trim()
    const to = row.to.trim()
    if (!from || !to || from.length > 200 || to.length > 200 || /\s/.test(from) || /[\s*]/.test(to) || !isValidWildcardPattern(from)) return 'invalidMapping'
    if (seen.has(from)) return 'duplicateMapping'
    seen.add(from)
  }
  return undefined
}

export function validAttributionActions(policy: AttributionPolicy): boolean {
  return (['high', 'low'] as const).every(tier => {
    const mapping = policy[`${tier}_model_mapping`]
    const groups = policy[`${tier}_group_ids`]
    return (mapping == null || !attributionMappingError(Object.entries(mapping).map(([from, to]) => ({ from, to })))) &&
      (groups == null || groups.length > 0 && groups.length <= 1000 && groups.every(id => Number.isSafeInteger(id) && id > 0) && new Set(groups).size === groups.length)
  })
}

export function cloneAttributionPolicy(policy: AttributionPolicy): AttributionPolicy {
  return {
    ...policy,
    expected_models: [...(policy.expected_models || [])],
    high_models: [...policy.high_models],
    low_models: [...policy.low_models],
    ...(policy.high_model_mapping != null ? { high_model_mapping: { ...policy.high_model_mapping } } : {}),
    ...(policy.low_model_mapping != null ? { low_model_mapping: { ...policy.low_model_mapping } } : {}),
    ...(policy.high_group_ids != null ? { high_group_ids: [...policy.high_group_ids] } : {}),
    ...(policy.low_group_ids != null ? { low_group_ids: [...policy.low_group_ids] } : {})
  }
}
