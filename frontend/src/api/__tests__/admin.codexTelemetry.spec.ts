import { beforeEach, describe, expect, it, vi } from 'vitest'
import { codexTelemetryAPI } from '../admin/codexTelemetry'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { get } }))

describe('codexTelemetryAPI', () => {
  beforeEach(() => { get.mockReset() })

  it('preserves filters and cancellation without using fingerprint endpoints', async () => {
    get.mockResolvedValue({ data: { items: [], total: 0 } })
    const controller = new AbortController()
    await codexTelemetryAPI.list({ account_id: 42, status: 'failed', type: 'metrics', os_family: 'macos', source: 'mixed', page: 2, page_size: 50 }, { signal: controller.signal })
    expect(get).toHaveBeenCalledWith('/admin/openai/telemetry-observations', {
      params: { account_id: 42, status: 'failed', type: 'metrics', os_family: 'macos', source: 'mixed', page: 2, page_size: 50 },
      signal: controller.signal,
    })
  })

  it('normalizes absent arrays so an empty API result cannot break rendering', async () => {
    get.mockResolvedValueOnce({ data: { items: null } })
    expect((await codexTelemetryAPI.list()).items).toEqual([])
    get.mockResolvedValueOnce({ data: { items: [null, { id: 1, event_names: null }, { id: 2, event_names: ['turn.completed', null, 7] }] } })
    expect((await codexTelemetryAPI.list()).items).toEqual([
      { id: 1, event_names: [], reasons: [], field_sources: {} }, { id: 2, event_names: ['turn.completed'], reasons: [], field_sources: {} },
    ])
  })

  it('normalizes diagnostic provenance without accepting unsupported sources', async () => {
    get.mockResolvedValue({ data: { items: [{ id: 1, event_names: [], reasons: [null, 7, 'unknown_os'], field_sources: { model: 'observed', shell: 'simulated', bad: 'claimed' } }] } })
    const [entry] = (await codexTelemetryAPI.list()).items
    expect(entry?.reasons).toEqual(['unknown_os'])
    expect(entry?.field_sources).toEqual({ model: 'observed', shell: 'simulated' })
  })

  it('keeps collection coverage separate from events and tolerates older responses', async () => {
    get.mockResolvedValueOnce({ data: { items: [], coverage: [
      null,
      { signal: 'auth_storage', type: 'metrics', status: 'awaiting_source', reason: 'client_auth_storage_not_on_responses_wire', event_names: ['codex.auth_storage.operation', null, 7] },
      { signal: 'unknown', type: 'logs', status: 'unavailable', reason: 'unknown' },
      { signal: 'skill_invocation', type: 'logs', status: 'collected', reason: 'unsupported' },
      { signal: 'skill_invocation', type: 'logs', status: 'unavailable', reason: 'client_skill_event_not_on_responses_wire', event_names: null },
    ] } })
    const result = await codexTelemetryAPI.list()
    expect(result.items).toEqual([])
    expect(result.coverage).toEqual([
      { signal: 'auth_storage', type: 'metrics', status: 'awaiting_source', reason: 'client_auth_storage_not_on_responses_wire', event_names: ['codex.auth_storage.operation'] },
      { signal: 'skill_invocation', type: 'logs', status: 'unavailable', reason: 'client_skill_event_not_on_responses_wire', event_names: [] },
    ])
    get.mockResolvedValueOnce({ data: { items: [], coverage: null } })
    expect((await codexTelemetryAPI.list()).coverage).toEqual([])
    get.mockResolvedValueOnce({ data: { items: [] } })
    expect((await codexTelemetryAPI.list()).coverage).toEqual([])
  })

  it('retains supported metric coverage awaiting client sources without fabricating deliveries', async () => {
    const coverage = [
      { signal: 'turn_critical_path', reason: 'client_critical_path_not_on_responses_wire', event_names: ['codex.turn.critical_path.capture'] },
      { signal: 'tool_declaration_updates', reason: 'client_tool_declaration_updates_not_on_responses_wire', event_names: ['codex.tools.exec_entry_updates'] },
      { signal: 'websocket_continuation', reason: 'client_websocket_continuation_not_on_responses_wire', event_names: ['codex.websocket.continuation'] },
      { signal: 'exec_command_timing', reason: 'client_exec_command_timing_not_on_responses_wire', event_names: ['codex.exec_command.duration_total_ms', 'codex.exec_command.calls'] },
      { signal: 'multi_agent_spawn', reason: 'client_multi_agent_spawn_not_on_responses_wire', event_names: ['codex.multi_agent.spawn.failure', 'codex.multi_agent.spawn.phase.duration_ms'] },
      { signal: 'additional_context', reason: 'client_additional_context_not_on_responses_wire', event_names: ['codex.additional_context', 'codex.additional_context.value_bytes'] },
      { signal: 'tool_registrations', reason: 'client_tool_registrations_not_on_responses_wire', event_names: ['codex.tools.registered'] },
      { signal: 'mcp_binding_catalog', reason: 'client_mcp_binding_catalog_not_on_responses_wire', event_names: ['codex.mcp.binding_catalog.raw_definition_json_bytes'] },
      { signal: 'model_search_discovery', reason: 'client_model_search_discovery_not_on_responses_wire', event_names: ['codex.model_search.discovery'] },
      { signal: 'artifact_operation_metrics', reason: 'client_artifact_operation_not_on_responses_wire', event_names: ['codex.artifact.operation.started', 'codex.artifact.operation.expected_output_count'] },
    ].map((entry) => ({ ...entry, type: 'metrics', status: 'awaiting_source' }))
    get.mockResolvedValueOnce({ data: { items: [], coverage } })
    const result = await codexTelemetryAPI.list()
    expect(result.coverage).toEqual(coverage)
    expect(result.items).toEqual([])
  })

  it('retains analytics coverage awaiting client sources without fabricating deliveries', async () => {
    const coverage = [
      { signal: 'used_dictation', type: 'analytics', status: 'awaiting_source', reason: 'client_dictation_source_not_on_responses_wire', event_names: ['codex_turn_event'] },
      { signal: 'guardian_v2_classification', type: 'analytics', status: 'awaiting_source', reason: 'client_guardian_v2_classification_not_on_responses_wire', event_names: ['codex_guardian_v2_classification'] },
      { signal: 'artifact_operation_analytics', type: 'analytics', status: 'awaiting_source', reason: 'client_artifact_operation_not_on_responses_wire', event_names: ['codex_artifact_operation'] },
    ]
    get.mockResolvedValueOnce({ data: { items: [], coverage } })
    const result = await codexTelemetryAPI.list()
    expect(result.coverage).toEqual(coverage)
    expect(result.items).toEqual([])
  })
})
