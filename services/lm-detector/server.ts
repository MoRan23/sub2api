import { loadDetector, type Detector } from './detector'
import type { Output } from './vendor/shared/types'

const maxBody = 20 * 1024 * 1024 // Three 1 MiB answers, including worst-case JSON escaping.
const maxAnswer = 1024 * 1024
const encoder = new TextEncoder()
export function createHandler(detector: Detector) {
  const headers = { 'Cache-Control': 'no-store', 'X-LM-Detector-Version': detector.version }
  const json = (value: unknown, status = 200) => Response.json(value, { status, headers })
  return async (req: Request): Promise<Response> => {
    const path = new URL(req.url).pathname
    if (req.headers.has('authorization') || req.headers.has('x-api-key')) return json({ error: 'credentials_not_accepted' }, 400)
    if (req.method === 'GET' && path === '/api/info') return json(detector.info)
    if (req.method === 'GET' && path === '/healthz') return json({ ready: true })
    if (req.headers.get('X-LM-Detector-Version') !== detector.version) return json({ error: 'detector_version_changed' }, 409)
    if (req.method === 'GET' && path === '/api/banks') return json({ shared: { models: detector.models } })
    if (req.method === 'GET' && path === '/api/challenges') return json({ challenges: detector.challenges() })
    if (req.method !== 'POST' || path !== '/api/analyze') return json({ error: 'not_found' }, 404)
    if (!req.headers.get('content-type')?.startsWith('application/json')) return json({ error: 'json_required' }, 415)
    try {
      const chunks: Uint8Array[] = []
      let size = 0
      const reader = req.body?.getReader()
      if (!reader) return json({ error: 'invalid_outputs' }, 400)
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        size += value.byteLength
        if (size > maxBody) { await reader.cancel(); return json({ error: 'body_too_large' }, 413) }
        chunks.push(value)
      }
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8'))
      if (!body || Object.keys(body).some(key => key !== 'outputs') || !Array.isArray(body.outputs) || body.outputs.length !== 3 ||
          !body.outputs.every((o: Output) => o && Object.keys(o).every(key => key === 'text' || key === 'expected_count') &&
            typeof o.text === 'string' && encoder.encode(o.text).byteLength <= maxAnswer &&
            Number.isInteger(o.expected_count) && o.expected_count >= 1 && o.expected_count <= 4096)) {
        return json({ error: 'invalid_outputs' }, 400)
      }
      // Do not trim, parse, cap or otherwise rewrite the model's answer here.
      return json(detector.analyze(body.outputs))
    } catch {
      // Never log or echo submitted answers.
      return json({ error: 'analysis_failed' }, 400)
    }
  }
}
if (import.meta.main) {
  // Throws before binding a port if any data/code integrity or binding check fails.
  const detector = loadDetector()
  Bun.serve({ hostname: '0.0.0.0', port: Number(process.env.PORT || 8080), maxRequestBodySize: maxBody, fetch: createHandler(detector) })
  console.log('LM Fingerpoint Detector ready: ' + detector.info.revision)
}
