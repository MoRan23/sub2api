import { describe, expect, test } from 'bun:test'
import { loadDetector, readPinned, validateArtifacts } from './detector'
import { createHandler } from './server'
import { analyzeSharedOutputs, type SharedDetector } from './vendor/shared/shared-detector'
import { parseReference, referenceSamples } from './vendor/shared/reference'
import type { Bank, Output } from './vendor/shared/types'

const detector = loadDetector()
const bank: Bank = JSON.parse(readPinned('data/unified_bank.json'))
const artifact: SharedDetector = JSON.parse(readPinned('data/shared_detector.json'))
const reference = readPinned('data/unified_reference.jsonl')
const outputs: Output[] = [...referenceSamples(parseReference(reference))]
  .filter(s => s.batch.model.id === 'gpt-6-astra').slice(0, 3)
  .map(s => ({ text: s.sample.text, expected_count: s.sample.expected_count }))
const request = (path: string, body?: unknown, version = detector.version) => new Request('http://detector' + path, {
  method: body ? 'POST' : 'GET',
  headers: { 'Content-Type': 'application/json', 'X-LM-Detector-Version': version },
  body: body ? JSON.stringify(body) : undefined,
})
describe('pinned official detector', () => {
  test('wraps the official complete-three-sample analysis without changing rankings or confidence', async () => {
    const expected = analyzeSharedOutputs(outputs, bank, artifact, { allowPartial: false })
    const response = await createHandler(detector)(request('/api/analyze', { outputs }))
    expect(response.status).toBe(200)
    expect(response.headers.get('X-LM-Detector-Version')).toBe(detector.version)
    const actual = await response.json()
    expect(actual.results).toEqual(expected.results)
    expect(actual.diagnostics).toEqual(expected.diagnostics)
    expect(actual.probability).toBe(expected.probability)
    expect(actual.probability_status).toBe('reference_calibrated')
    expect(actual.decision).toBe('not_confirmed')
    expect(actual.detector).toEqual(detector.info)
  })
  test('keeps every character of answers and delegates validity to the official function', async () => {
    let received: Output[] | undefined
    const handler = createHandler({ ...detector, analyze: o => { received = o; return detector.analyze(o) } })
    const original = outputs.map(o => ({ ...o, text: ' 说明:\n' + o.text + '\n-1 999 0003\t' }))
    await handler(request('/api/analyze', { outputs: original }))
    expect(received).toEqual(original)
    const invalid = await (await handler(request('/api/analyze', { outputs: [outputs[0], outputs[1], { text: '', expected_count: 300 }] }))).json()
    expect(invalid.used_outputs).toBe(2)
    expect(invalid.probability).toBeNull()
    expect(invalid.decision).toBe('unscorable')
  })
  test('refuses old versions, partial batches, credentials and unknown payload fields', async () => {
    const handler = createHandler(detector)
    expect((await handler(request('/api/analyze', { outputs }, 'old'))).status).toBe(409)
    expect((await handler(request('/api/analyze', { outputs: outputs.slice(0, 2) }))).status).toBe(400)
    expect((await handler(request('/api/analyze', { outputs, api_key: 'synthetic-secret' }))).status).toBe(400)
    const req = request('/api/info')
    req.headers.set('Authorization', 'Bearer synthetic-secret')
    expect((await handler(req)).status).toBe(400)
  })
  test('generates exactly three original challenges and lists the enrolled model IDs', async () => {
    const handler = createHandler(detector)
    const result = await (await handler(request('/api/challenges'))).json()
    expect(result.challenges).toHaveLength(3)
    expect(new Set(result.challenges.map((c: {prompt: string}) => c.prompt)).size).toBe(3)
    expect(detector.models.some(m => m.id === 'gpt-6-astra')).toBe(true)
    expect(detector.models.some(m => m.id === 'gpt-6.1-sol')).toBe(true)
  })
  test('fails closed for missing calibration, mismatched bank and altered references', () => {
    expect(() => validateArtifacts(bank, { ...artifact, calibration: null }, reference)).toThrow()
    expect(() => validateArtifacts({ ...bank, built_at: 'different' }, artifact, reference)).toThrow()
    expect(() => validateArtifacts(bank, artifact, reference + '\n')).toThrow()
    const uncalibrated = analyzeSharedOutputs(outputs, bank, { ...artifact, calibration: null })
    expect(uncalibrated.probability).toBeNull()
    expect(uncalibrated.probability_status).toBe('unavailable')
  })
})
