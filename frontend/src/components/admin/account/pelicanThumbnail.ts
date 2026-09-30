import { candyTestsAPI, PELICAN_TEST_PROMPT_VERSION } from '@/api/admin/candyTests'

// Keep only the selected HTML, not the raw replies or the other history entries.
// A bounded cache avoids refetching while virtual rows leave and enter the view.
const documents = new Map<string, string>()
const maxCachedDocuments = 20

export async function loadPelicanThumbnail(accountId: number, itemId: number, signal: AbortSignal): Promise<string> {
  const key = `${accountId}:${itemId}`
  const cached = documents.get(key)
  if (cached) {
    documents.delete(key)
    documents.set(key, cached)
    return cached
  }
  const result = await candyTestsAPI.history(accountId, signal)
  const latest = result.items?.[0]
  // Never pick an older success if the newest request failed or the account
  // summary changed while this read was in flight.
  if (signal.aborted || latest?.id !== itemId || latest.prompt_version !== PELICAN_TEST_PROMPT_VERSION || latest.status !== 'generated' || !latest.html) return ''
  documents.set(key, latest.html)
  if (documents.size > maxCachedDocuments) documents.delete(documents.keys().next().value!)
  return latest.html
}
