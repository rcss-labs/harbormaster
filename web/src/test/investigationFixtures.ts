import { http, HttpResponse } from 'msw'
import type { InvestigationDTO, InvestigationDetailDTO } from '../api/investigations'
import { server } from './msw'
import { ME_ADMIN, SERVICE_PAYMENTS } from './fixtures'

export const INVESTIGATION_DONE: InvestigationDetailDTO = {
  id: 7,
  state: 'done',
  reason: '',
  subject: { kind: 'service', service_id: SERVICE_PAYMENTS.id, service: 'payments', cluster: 'prod-main', cluster_id: 'c1', account_id: '', region: '' },
  alert: { kind: 'service_failing', title: 'payments: 0 of 2 tasks running' },
  requested_at: '2026-11-07T11:30:00Z',
  started_at: '2026-11-07T11:30:05Z',
  finished_at: '2026-11-07T11:31:00Z',
  summary: 'The tasks fail to start because the image tag does not exist.\nA second line that should not appear in the list.',
  summary_replaced: false,
  members: [],
  evidence: [
    { ordinal: 1, tool: 'get_service_events', action: 'view', service_id: SERVICE_PAYMENTS.id, service: 'payments', note: 'Start failures began after the deploy.', excerpt: 'CannotPullContainerError: manifest unknown' },
    { ordinal: 2, tool: 'search_logs', action: 'logs', service_id: SERVICE_PAYMENTS.id, service: 'payments', note: 'No application output.', excerpt: 'no log lines in the last 10 minutes' },
  ],
  evidence_hidden: 0,
}

export const toListItem = (detail: InvestigationDetailDTO): InvestigationDTO => {
  const item: Partial<InvestigationDetailDTO> = { ...detail }
  delete item.members
  delete item.evidence
  delete item.evidence_hidden
  return item as InvestigationDTO
}

/** Turns the agent on for the signed-in admin and serves the three investigation routes. Returns the paths requested. */
export function mockInvestigations(details: InvestigationDetailDTO[] = [INVESTIGATION_DONE], agentEnabled = true) {
  const requested: string[] = []
  server.use(
    http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: agentEnabled })),
    http.get('/api/services/:id/investigations', ({ request }) => {
      requested.push(new URL(request.url).pathname)
      return HttpResponse.json({ items: details.map(toListItem) })
    }),
    http.get('/api/investigations/by-incident/:incidentId', ({ request, params }) => {
      requested.push(new URL(request.url).pathname)
      const found = details.find((detail) => String(detail.id) === params.incidentId)
      return found ? HttpResponse.json(found) : HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })
    }),
    http.get('/api/investigations/:id', ({ request, params }) => {
      requested.push(new URL(request.url).pathname)
      const found = details.find((detail) => String(detail.id) === params.id)
      return found ? HttpResponse.json(found) : HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })
    }),
  )
  return requested
}
