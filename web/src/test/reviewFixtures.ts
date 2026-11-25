import { http, HttpResponse } from 'msw'
import type { ReviewRunDTO, ReviewRunDetailDTO } from '../api/reviews'
import { server } from './msw'
import { ME_ADMIN, SERVICE_PAYMENTS } from './fixtures'

export const REVIEW_RUN_DONE: ReviewRunDetailDTO = {
  id: 31,
  review_id: 4,
  name: 'Weekly <b>cost</b> check',
  type: 'cost',
  manual: false,
  state: 'done',
  reason: '',
  period_label: 'Week of 16 Nov 2026',
  scope_label: 'Tag: production',
  services_total: 12,
  scheduled_for: '2026-11-23T08:00:00Z',
  requested_at: '2026-11-23T08:00:01Z',
  started_at: '2026-11-23T08:00:05Z',
  finished_at: '2026-11-23T08:04:00Z',
  summary: 'Spend rose 12% on three services.\nA second line that should not appear in the list.',
  summary_replaced: false,
  test: false,
  evidence: [
    { ordinal: 1, tool: 'get_cost_breakdown', action: 'view', service_id: SERVICE_PAYMENTS.id, service: 'payments', note: 'Fargate hours doubled.', excerpt: 'payments: 2 -> 4 tasks' },
  ],
  evidence_hidden: 0,
}

export const toReviewListItem = (detail: ReviewRunDetailDTO): ReviewRunDTO => {
  const item: Partial<ReviewRunDetailDTO> = { ...detail }
  delete item.evidence
  delete item.evidence_hidden
  return item as ReviewRunDTO
}

/** Turns reviews on (or off) for the signed-in admin and serves the two review routes. Returns the URLs requested. */
export function mockReviews(details: ReviewRunDetailDTO[] = [REVIEW_RUN_DONE], reviewsEnabled = true, pageSize = 50) {
  const requested: string[] = []
  server.use(
    http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: reviewsEnabled, reviews_enabled: reviewsEnabled })),
    http.get('/api/reviews/runs/:id', ({ request, params }) => {
      requested.push(new URL(request.url).pathname)
      const found = details.find((detail) => String(detail.id) === params.id)
      return found ? HttpResponse.json(found) : HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })
    }),
    http.get('/api/reviews/runs', ({ request }) => {
      const url = new URL(request.url)
      requested.push(url.pathname + url.search)
      const start = Number(url.searchParams.get('cursor') || 0)
      const page = details.slice(start, start + pageSize).map(toReviewListItem)
      const next = start + pageSize < details.length ? String(start + pageSize) : ''
      return HttpResponse.json({ items: page, next_cursor: next })
    }),
  )
  return requested
}
