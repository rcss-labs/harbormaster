import { http, HttpResponse } from 'msw'
import type { AgentSettingsDTO, ReviewDTO, ReviewInput, ReviewPreviewDTO, ReviewRunRow, ReviewRunsPage } from '../api/agent'
import { ME_ADMIN } from './fixtures'
import { CORE_SERVICES_CLUSTER, selector } from './notificationsFixtures'
import { server } from './msw'

export const REVIEW_SETTINGS: AgentSettingsDTO = {
  version: 3,
  monthly_budget_usd: '25.00',
  unredacted_providers: [],
  role_models: {
    chat: { provider: 'anthropic', model: 'model-a', available: true, max_question_usd: '0.60', allowance_below_question: false },
    reviewer: { provider: 'anthropic', model: 'model-a', available: true, max_question_usd: '0.84', allowance_below_question: false },
  },
  catalogue: {
    providers: [
      {
        provider: 'anthropic',
        models: [
          { id: 'model-a', input_per_mtok_micro_usd: 3_000_000, output_per_mtok_micro_usd: 15_000_000, context_tokens: 200000 },
          { id: 'model-b', input_per_mtok_micro_usd: 250_000, output_per_mtok_micro_usd: 1_250_000, context_tokens: 100000 },
        ],
      },
    ],
  },
  agent_reachable: true,
  spend: { month: '2026-11', settled_usd: '1.25', reserved_usd: '0.60' },
  identity: { username: 'harbormaster-agent', permissions: [{ tag_id: 3, tag_name: 'Prod', logs: false }] },
  investigator: { window_minutes: 15, per_hour: 20, concurrent: 2, max_wait_minutes: 15 },
  reviews: {
    enabled: true,
    per_day: 24,
    max_late_minutes: 360,
    max_reviews: 20,
    types: [
      { id: 'cost', label: 'Cost' },
      { id: 'security', label: 'Security findings' },
      { id: 'reliability', label: 'Reliability' },
      { id: 'capacity', label: 'Capacity' },
    ],
    supported_types: ['cost', 'security', 'reliability'],
    worst_case_run_usd: '0.84',
    custom_enabled: false,
    max_instruction_chars: 2000,
  },
}

export const REVIEW_WEEKLY_COST: ReviewDTO = {
  id: 1,
  name: 'Weekly cost',
  type: 'cost',
  enabled: true,
  schedule: { cadence: 'weekly', at_minute: 480, weekday: 1, month_day: null, timezone: 'Europe/Berlin' },
  scope: [selector({ ...CORE_SERVICES_CLUSTER })],
  routes: [{ id: 2, name: 'core-services to #core-alerts', destination: 'slack', enabled: true }],
  version: 4,
  next_run_at: '2026-11-30T07:00:00Z',
  last_run: { id: 41, state: 'done', reason: '', finished_at: '2026-11-23T07:02:00Z' },
  worst_case_month_usd: '3.36',
  instructions: '',
}

export const REVIEW_MONTHLY_SECURITY: ReviewDTO = {
  id: 2,
  name: 'Monthly security',
  type: 'security',
  enabled: false,
  schedule: { cadence: 'monthly', at_minute: 390, weekday: null, month_day: 15, timezone: 'UTC' },
  scope: [],
  routes: [{ id: 4, name: 'Staging account by email', destination: 'email', enabled: true }],
  version: 1,
  next_run_at: null,
  last_run: null,
  worst_case_month_usd: null,
  instructions: '',
}

/** The same settings with the operator's switch on: the type list ends with Custom and the agent supports it. */
export const REVIEW_SETTINGS_CUSTOM: AgentSettingsDTO = {
  ...REVIEW_SETTINGS,
  reviews: {
    ...REVIEW_SETTINGS.reviews,
    custom_enabled: true,
    types: [...REVIEW_SETTINGS.reviews.types, { id: 'custom', label: 'Custom' }],
    supported_types: [...REVIEW_SETTINGS.reviews.supported_types, 'custom'],
  },
}

export const REVIEW_CUSTOM: ReviewDTO = {
  ...REVIEW_MONTHLY_SECURITY,
  id: 3,
  name: 'Team spend',
  type: 'custom',
  instructions: 'Look at spend by team.\nKeep it short.',
}

export const PREVIEW_SMALL: ReviewPreviewDTO = { services_total: 12, services_unseen: 0, next_run_at: '2026-11-30T07:00:00Z', worst_case_month_usd: '3.36', instructions_problem: '' }

export const reviewRun = (id: number, fields: Partial<ReviewRunRow> = {}): ReviewRunRow => ({
  id,
  review_id: 1,
  name: 'Weekly cost',
  type: 'cost',
  manual: false,
  state: 'done',
  reason: '',
  period_label: 'Week 47, 2026',
  scope_label: 'core-services',
  services_total: 12,
  scheduled_for: '2026-11-23T07:00:00Z',
  requested_at: '2026-11-23T07:00:00Z',
  started_at: '2026-11-23T07:00:05Z',
  finished_at: '2026-11-23T07:02:00Z',
  summary: '',
  summary_replaced: false,
  provider: 'anthropic',
  model: 'model-a',
  attempts: 1,
  cost_usd: '0.31',
  test: false,
  instructions: '',
  ...fields,
})

interface ReviewMocks {
  settings?: AgentSettingsDTO
  reviews?: ReviewDTO[]
  preview?: ReviewPreviewDTO
  runs?: ReviewRunsPage
  previews?: ReviewInput[]
}

/** The agent on, an admin signed in, and the review endpoints answering from the given lists. */
export function useReviewMocks({ settings = REVIEW_SETTINGS, reviews = [REVIEW_WEEKLY_COST, REVIEW_MONTHLY_SECURITY], preview = PREVIEW_SMALL, runs = { items: [], next_cursor: '' }, previews = [] }: ReviewMocks = {}) {
  server.use(
    http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, ai_chat: true, reviews_enabled: true })),
    http.get('/api/agent/settings', () => HttpResponse.json(settings)),
    http.get('/api/agent/usage', () => HttpResponse.json({ month: '2026-11', days: [] })),
    http.get('/api/agent/investigations', () => HttpResponse.json({ items: [], next_cursor: '' })),
    http.get('/api/agent/reviews', () => HttpResponse.json({ items: reviews })),
    http.get('/api/agent/reviews/runs', () => HttpResponse.json(runs)),
    http.post('/api/agent/reviews/preview', async ({ request }) => {
      previews.push((await request.json()) as ReviewInput)
      return HttpResponse.json(preview)
    }),
  )
}
