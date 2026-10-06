import { readFileSync } from 'node:fs'
import { gunzipSync } from 'node:zlib'
import { createHash } from 'node:crypto'
import { generateChallenges } from './vendor/shared/challenge-browser.js'
import { analyzeSharedOutputs, calibrateRanking, supportsSharedDetector, type SharedDetector } from './vendor/shared/shared-detector'
import { parseReference, referenceSamples } from './vendor/shared/reference'
import type { Bank, Output } from './vendor/shared/types'
import upstream from './upstream.json'

const digest = (bytes: string | Uint8Array) => createHash('sha256').update(bytes).digest('hex')
export function readPinned(path: keyof typeof upstream.files): string {
  const compressed = path.startsWith('data/')
  const bytes = readFileSync(new URL('./vendor/' + path + (compressed ? '.gz' : ''), import.meta.url))
  const plain = compressed ? gunzipSync(bytes) : bytes
  if (digest(plain) !== upstream.files[path]) throw new Error('Pinned artifact integrity mismatch: ' + path)
  return plain.toString('utf8')
}
export function validateArtifacts(bank: Bank, detector: SharedDetector, reference: string) {
  if (bank.reference_sha256 !== digest(reference) ||
      detector.source_reference_sha256 !== bank.reference_sha256 ||
      !supportsSharedDetector(bank, detector) ||
      !calibrateRanking(detector.model_ids.map(() => 0), detector)) {
    throw new Error('Detector, reference bank or calibration binding mismatch')
  }
  const counts = new Map<string, number>()
  for (const { batch } of referenceSamples(parseReference(reference))) {
    counts.set(batch.model.id, (counts.get(batch.model.id) ?? 0) + 1)
  }
  if (counts.size !== bank.models.length || bank.models.some(m => counts.get(m.id) !== m.response_count)) {
    throw new Error('Reference sample count mismatch')
  }
}
export function loadDetector() {
  // Vendor code is pinned with the same manifest as the bundled data.
  for (const path of Object.keys(upstream.files) as (keyof typeof upstream.files)[]) {
    if (!path.startsWith('data/')) readPinned(path)
  }
  const bank: Bank = JSON.parse(readPinned('data/unified_bank.json'))
  const artifact: SharedDetector = JSON.parse(readPinned('data/shared_detector.json'))
  validateArtifacts(bank, artifact, readPinned('data/unified_reference.jsonl'))
  const info = {
    provider: 'lm_fingerprint_detector', protocol: 1, revision: upstream.revision,
    algorithm: artifact.schema, bank_built_at: artifact.bank_built_at,
    reference_sha256: artifact.source_reference_sha256,
    ranker_sha256: artifact.base_sha256, calibration_sha256: artifact.calibration_sha256!,
  }
  const version = [info.revision, info.ranker_sha256, info.reference_sha256, info.calibration_sha256].join(':')
  return {
    info, version,
    models: bank.models.map(m => ({ id: m.id })),
    challenges: () => generateChallenges(3),
    analyze: (outputs: Output[]) => ({
      ...analyzeSharedOutputs(outputs, bank, artifact, { allowPartial: false }), detector: info,
    }),
  }
}
export type Detector = ReturnType<typeof loadDetector>
