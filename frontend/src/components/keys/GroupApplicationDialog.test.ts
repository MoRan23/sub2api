import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/vue'
import { createPinia } from 'pinia'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import GroupApplicationDialog from './GroupApplicationDialog.vue'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  const translations: Record<string, string> = {
    'common.refresh': 'Refresh',
    'common.submitting': 'Submitting',
    'common.close': 'Close',
    'common.loading': 'Loading',
    'common.error': 'Error',
    'groupApplications.title': 'Apply for a Group',
    'groupApplications.newApplication': 'New application',
    'groupApplications.emailNotice': 'Email notice',
    'groupApplications.group': 'Group',
    'groupApplications.selectGroup': 'Select a group',
    'groupApplications.contactEmail': 'Contact email',
    'groupApplications.reason': 'Application reason',
    'groupApplications.submit': 'Submit application',
    'groupApplications.history': 'Application progress',
    'groupApplications.noHistory': 'No applications yet',
    'groupApplications.submitted': 'Application submitted',
    'groupApplications.decisionReason': 'Decision reason',
    'groupApplications.awaitingReplyHint': 'Awaiting reply',
    'groupApplications.emailFailed': 'Email failed',
    'groupApplications.status.pending': 'Pending',
  }

  return {
    ...actual,
    useI18n: () => ({
      locale: { value: 'en' },
      t: (key: string) => translations[key] ?? key,
    }),
  }
})

let submittedBody: Record<string, unknown> | null = null

const server = setupServer(
  http.get('*/api/v1/group-applications/options', () => HttpResponse.json({ code: 0, data: [{ group_id: 12, group_name: 'Private Pro', has_active: false, already_completed: false }] })),
  http.get('*/api/v1/group-applications', () => HttpResponse.json({ code: 0, data: submittedBody ? [{ id: 1, user_id: 7, group_id: 12, group_name: 'Private Pro', contact_email: 'user@example.com', reason: 'Need access for production workloads', locale: 'en', status: 'pending', attachment_id: 9, created_at: '2026-08-27T00:00:00Z', updated_at: '2026-08-27T00:00:00Z' }] : [] })),
  http.post('*/api/v1/group-applications', async ({ request }) => {
    submittedBody = await request.json() as Record<string, unknown>
    return HttpResponse.json({ code: 0, data: { id: 1 } })
  })
)

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => { submittedBody = null; server.resetHandlers() })
afterAll(() => server.close())

describe('GroupApplicationDialog', () => {
  it('loads applyable groups and submits the required contact fields', async () => {
    render(GroupApplicationDialog, { props: { show: true }, global: { plugins: [createPinia()] } })
    expect(await screen.findByText('Private Pro')).toBeTruthy()
    await fireEvent.update(screen.getByLabelText('Contact email'), 'user@example.com')
    await fireEvent.update(screen.getByLabelText(/^Application reason/), 'Need access for production workloads')
    await fireEvent.click(screen.getByRole('button', { name: 'Submit application' }))

    await waitFor(() => expect(submittedBody).toMatchObject({ group_id: 12, contact_email: 'user@example.com', reason: 'Need access for production workloads', locale: 'en' }))
    expect(await screen.findByText('Need access for production workloads')).toBeTruthy()
  })
})
