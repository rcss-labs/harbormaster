import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { AgentInvestigationDTO, AgentSettingsDTO, AgentUsageDTO } from '../../../api/agent'
import { ME_ADMIN } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const CATALOGUE: AgentSettingsDTO['catalogue'] = {
  providers: [
    {
      provider: 'anthropic',
      models: [
        { id: 'model-a', input_per_mtok_micro_usd: 3_000_000, output_per_mtok_micro_usd: 15_000_000, context_tokens: 200000 },
        { id: 'model-b', input_per_mtok_micro_usd: 250_000, output_per_mtok_micro_usd: 1_250_000, context_tokens: 100000 },
      ],
    },
    { provider: 'openai', models: [{ id: 'model-c', input_per_mtok_micro_usd: 1_000_000, output_per_mtok_micro_usd: 4_000_000, context_tokens: 128000 }] },
  ],
}

const SETTINGS: AgentSettingsDTO = {
  version: 3,
  monthly_budget_usd: '25.00',
  unredacted_providers: [],
  role_models: { chat: { provider: 'anthropic', model: 'model-a', available: true, max_question_usd: '0.60', allowance_below_question: false } },
  catalogue: CATALOGUE,
  agent_reachable: true,
  spend: { month: '2026-11', settled_usd: '1.25', reserved_usd: '0.60' },
  identity: { username: 'harbormaster-agent', permissions: [{ tag_id: 3, tag_name: 'Prod', logs: false }] },
  investigator: { window_minutes: 15, per_hour: 20, concurrent: 2, max_wait_minutes: 15 },
  reviews: {
    enabled: true,
    per_day: 24,
    max_late_minutes: 360,
    max_reviews: 20,
    types: [{ id: 'cost', label: 'Cost' }],
    supported_types: ['cost'],
    worst_case_run_usd: null,
    custom_enabled: false,
    max_instruction_chars: 2000,
  },
}

const investigation = (id: number, fields: Partial<AgentInvestigationDTO> = {}): AgentInvestigationDTO => ({
  id,
  state: 'done',
  reason: '',
  subject: { kind: 'service', service_id: 'core-payments-api', service: 'payments-api', cluster: 'core-services', cluster_id: 'c1', account_id: '482109375512', region: 'us-east-1' },
  alert: { kind: 'service_failing', title: 'payments-api: service failing' },
  requested_at: '2026-11-24T10:00:00Z',
  started_at: '2026-11-24T10:00:05Z',
  finished_at: '2026-11-24T10:01:00Z',
  summary: '',
  summary_replaced: false,
  provider: 'anthropic',
  model: 'model-a',
  attempts: 1,
  cost_usd: '0.12',
  ...fields,
})

const USAGE: AgentUsageDTO = {
  month: '2026-11',
  days: [{ day: '2026-11-24', role: 'chat', runs: 4, cost_usd: '0.42', input_tokens: 1200, output_tokens: 340 }],
}

const useAgentMocks = (settings: AgentSettingsDTO = SETTINGS) =>
  server.use(
    http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, ai_chat: true })),
    http.get('/api/agent/settings', () => HttpResponse.json(settings)),
    http.get('/api/agent/usage', () => HttpResponse.json(USAGE)),
    http.get('/api/agent/investigations', () => HttpResponse.json({ items: [], next_cursor: '' })),
    http.get('/api/agent/reviews', () => HttpResponse.json({ items: [] })),
  )

describe('AgentSettings', () => {
  it('tab absent when the agent is off', async () => {
    renderApp('/settings/general')
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'AI agent' })).not.toBeInTheDocument()
    window.history.replaceState(null, '', '/')
  })

  it('shows the tab, status, and usage when the agent is on', async () => {
    useAgentMocks()
    renderApp('/settings/general')
    expect(await screen.findByRole('link', { name: 'AI agent' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('link', { name: 'AI agent' }))
    expect(await screen.findByText('The agent is reachable.')).toBeInTheDocument()
    expect(screen.getByText('Spent $1.25 and reserved $0.60 of $25.00 in 2026-11.')).toBeInTheDocument()
    const usage = within(await screen.findByRole('table'))
    expect(usage.getByText('2026-11-24')).toBeInTheDocument()
    expect(usage.getByText('$0.42')).toBeInTheDocument()
  })

  it('says what one question reserves with the chosen model', async () => {
    useAgentMocks()
    renderApp('/settings/agent')
    expect(await screen.findByText('One question reserves up to $0.60 with this model.')).toBeInTheDocument()
    expect(screen.queryByText(/every question will be refused/)).not.toBeInTheDocument()
  })

  it('warns when the allowance left is below what one question reserves', async () => {
    const chat = { provider: 'anthropic', model: 'model-a', available: true, max_question_usd: '0.60', allowance_below_question: true }
    useAgentMocks({ ...SETTINGS, monthly_budget_usd: '0.50', role_models: { chat } })
    renderApp('/settings/agent')
    expect(await screen.findByText('The allowance is below what one question reserves, so every question will be refused.')).toBeInTheDocument()
  })

  it('warns as soon as a typed allowance is below what one question reserves', async () => {
    useAgentMocks()
    renderApp('/settings/agent')
    const allowance = await screen.findByLabelText('Monthly allowance in US dollars')
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '0.50')
    expect(await screen.findByText('The allowance is below what one question reserves, so every question will be refused.')).toBeInTheDocument()
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '5')
    await waitFor(() => expect(screen.queryByText(/every question will be refused/)).not.toBeInTheDocument())
  })

  it('does not warn when a typed allowance leaves exactly what one question reserves', async () => {
    // 1.15 - 0.55 is 0.5999999999999999 in floating point; in cents it is exactly the 0.60 one question reserves.
    useAgentMocks({ ...SETTINGS, spend: { month: '2026-11', settled_usd: '0.00', reserved_usd: '0.55' } })
    renderApp('/settings/agent')
    const allowance = await screen.findByLabelText('Monthly allowance in US dollars')
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '1.15')
    expect(screen.getByLabelText('Monthly allowance in US dollars')).toHaveValue('1.15')
    expect(screen.queryByText(/every question will be refused/)).not.toBeInTheDocument()
  })

  it('does not state a reservation while the model is not known', async () => {
    const chat = { provider: 'anthropic', model: 'model-a', available: true, max_question_usd: '', allowance_below_question: false }
    useAgentMocks({ ...SETTINGS, role_models: { chat } })
    renderApp('/settings/agent')
    await screen.findByLabelText('Monthly allowance in US dollars')
    expect(screen.queryByText(/One question reserves/)).not.toBeInTheDocument()
  })

  it('saves model and allowance', async () => {
    useAgentMocks()
    const bodies: unknown[] = []
    server.use(
      http.put('/api/agent/settings', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ ...SETTINGS, monthly_budget_usd: '40.00' })
      }),
    )
    renderApp('/settings/agent')
    const model = await screen.findByLabelText('Model')
    expect(screen.getByText('Input $3.00 per million tokens, output $15.00 per million tokens.')).toBeInTheDocument()
    await userEvent.selectOptions(model, 'model-b')
    expect(screen.getByText('Input $0.25 per million tokens, output $1.25 per million tokens.')).toBeInTheDocument()
    const allowance = screen.getByLabelText('Monthly allowance in US dollars')
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '40')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(bodies).toEqual([{ monthly_budget_usd: '40', unredacted_providers: [], role_models: { chat: { provider: 'anthropic', model: 'model-b' }, investigator: null, reviewer: null }, version: 3 }]),
    )
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
  })

  it('shows a stale model warning and still saves other settings with it unchanged', async () => {
    const stale = { ...SETTINGS, role_models: { chat: { provider: 'anthropic', model: 'retired', available: false, max_question_usd: '', allowance_below_question: false } } }
    useAgentMocks(stale)
    const bodies: unknown[] = []
    server.use(
      http.put('/api/agent/settings', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ ...stale, monthly_budget_usd: '30' })
      }),
    )
    renderApp('/settings/agent')
    expect(await screen.findByText('This model is no longer offered by the operator. Choose another.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    const allowance = screen.getByLabelText('Monthly allowance in US dollars')
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '30')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(bodies).toEqual([{ monthly_budget_usd: '30', unredacted_providers: [], role_models: { chat: { provider: 'anthropic', model: 'retired' }, investigator: null, reviewer: null }, version: 3 }]),
    )
  })

  it('saves an allowance change without a chat model and omits chat', async () => {
    const none = { ...SETTINGS, role_models: {} }
    useAgentMocks(none)
    const bodies: unknown[] = []
    server.use(
      http.put('/api/agent/settings', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json(none)
      }),
    )
    renderApp('/settings/agent')
    const allowance = await screen.findByLabelText('Monthly allowance in US dollars')
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '12.50')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(bodies).toEqual([{ monthly_budget_usd: '12.50', unredacted_providers: [], role_models: { investigator: null, reviewer: null }, version: 3 }]))
  })

  it('on a conflict shows the message, loads the stored settings and keeps what was typed', async () => {
    let stored: AgentSettingsDTO = SETTINGS
    const bodies: { version: number }[] = []
    server.use(
      http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, ai_chat: true })),
      http.get('/api/agent/settings', () => HttpResponse.json(stored)),
      http.get('/api/agent/usage', () => HttpResponse.json(USAGE)),
      http.put('/api/agent/settings', async ({ request }) => {
        const body = (await request.json()) as { version: number }
        bodies.push(body)
        if (body.version !== stored.version) {
          return HttpResponse.json(
            { error: { code: 'settings_changed', message: 'These settings were changed by someone else. Reload and try again.' } },
            { status: 409 },
          )
        }
        stored = { ...stored, version: 5, monthly_budget_usd: '40.00' }
        return HttpResponse.json(stored)
      }),
    )
    renderApp('/settings/agent')
    const allowance = await screen.findByLabelText('Monthly allowance in US dollars')
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '40')
    // Someone else saves first: redaction lifted and a new allowance.
    stored = { ...SETTINGS, version: 4, monthly_budget_usd: '30.00', unredacted_providers: ['openai'] }
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findAllByText('These settings were changed by someone else. Reload and try again.')).not.toHaveLength(0)
    // The stored values are loaded, and the typed amount is still in the form.
    expect(await screen.findByText('Spent $1.25 and reserved $0.60 of $30.00 in 2026-11.')).toBeInTheDocument()
    expect(screen.getByLabelText('Monthly allowance in US dollars')).toHaveValue('40')
    expect(screen.getByRole('checkbox', { name: 'Send environment values to OpenAI' })).toBeChecked()
    // Saving again sends the version just loaded.
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(bodies.map((body) => body.version)).toEqual([3, 4]))
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
    expect(screen.queryByText('These settings were changed by someone else. Reload and try again.')).not.toBeInTheDocument()
  })

  it('on a conflict merges redaction per provider and keeps only what was touched', async () => {
    const loaded: AgentSettingsDTO = { ...SETTINGS, unredacted_providers: ['openai'] }
    let stored: AgentSettingsDTO = loaded
    const bodies: { version: number; unredacted_providers: string[]; monthly_budget_usd: string; role_models: unknown }[] = []
    server.use(
      http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, ai_chat: true })),
      http.get('/api/agent/settings', () => HttpResponse.json(stored)),
      http.get('/api/agent/usage', () => HttpResponse.json(USAGE)),
      http.put('/api/agent/settings', async ({ request }) => {
        const body = (await request.json()) as (typeof bodies)[number]
        bodies.push(body)
        if (body.version !== stored.version) {
          return HttpResponse.json(
            { error: { code: 'settings_changed', message: 'These settings were changed by someone else. Reload and try again.' } },
            { status: 409 },
          )
        }
        return HttpResponse.json(stored)
      }),
    )
    renderApp('/settings/agent')
    // This admin only ticks Anthropic.
    const anthropic = await screen.findByRole('checkbox', { name: 'Send environment values to Anthropic' })
    await userEvent.click(anthropic)
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Send environment values' }))
    // Another admin turned openai's values off again, changed the allowance and the chat model.
    stored = {
      ...SETTINGS,
      version: 4,
      unredacted_providers: [],
      monthly_budget_usd: '30.00',
      role_models: { chat: { provider: 'anthropic', model: 'model-b', available: true, max_question_usd: '0.06', allowance_below_question: false } },
    }
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Spent $1.25 and reserved $0.60 of $30.00 in 2026-11.')).toBeInTheDocument()
    // openai stays off, anthropic (this admin's tick) stays on; untouched budget and model follow the stored values.
    expect(screen.getByRole('checkbox', { name: 'Send environment values to OpenAI' })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Send environment values to Anthropic' })).toBeChecked()
    expect(screen.getByLabelText('Monthly allowance in US dollars')).toHaveValue('30.00')
    expect(screen.getByLabelText('Model')).toHaveValue('model-b')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({
      version: 4,
      monthly_budget_usd: '30.00',
      unredacted_providers: ['anthropic'],
      role_models: { chat: { provider: 'anthropic', model: 'model-b' }, investigator: null, reviewer: null },
    })
  })

  it('on a conflict a touched allowance and model stay as typed while the rest follows the stored values', async () => {
    let stored: AgentSettingsDTO = SETTINGS
    const bodies: unknown[] = []
    server.use(
      http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, ai_chat: true })),
      http.get('/api/agent/settings', () => HttpResponse.json(stored)),
      http.get('/api/agent/usage', () => HttpResponse.json(USAGE)),
      http.put('/api/agent/settings', async ({ request }) => {
        const body = (await request.json()) as { version: number }
        bodies.push(body)
        if (body.version !== stored.version) {
          return HttpResponse.json({ error: { code: 'settings_changed', message: 'Changed.' } }, { status: 409 })
        }
        return HttpResponse.json(stored)
      }),
    )
    renderApp('/settings/agent')
    await userEvent.selectOptions(await screen.findByLabelText('Model'), 'model-b')
    // Meanwhile someone else lifts redaction for openai and changes the allowance.
    stored = { ...SETTINGS, version: 4, monthly_budget_usd: '30.00', unredacted_providers: ['openai'] }
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Spent $1.25 and reserved $0.60 of $30.00 in 2026-11.')).toBeInTheDocument()
    expect(screen.getByLabelText('Model')).toHaveValue('model-b') // touched: stays
    expect(screen.getByLabelText('Monthly allowance in US dollars')).toHaveValue('30.00') // untouched: follows
    expect(screen.getByRole('checkbox', { name: 'Send environment values to OpenAI' })).toBeChecked() // untouched: follows
  })

  it('a provider without a model blocks Save', async () => {
    useAgentMocks({ ...SETTINGS, role_models: {} })
    renderApp('/settings/agent')
    await userEvent.selectOptions(await screen.findByLabelText('Provider'), 'openai')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    await userEvent.selectOptions(screen.getByLabelText('Model'), 'model-c')
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('when the agent is unreachable, says so and keeps allowance, usage and stored redaction usable', async () => {
    useAgentMocks({
      ...SETTINGS,
      agent_reachable: false,
      catalogue: { providers: [] },
      role_models: {},
      unredacted_providers: ['openai'],
    })
    const bodies: unknown[] = []
    server.use(
      http.put('/api/agent/settings', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ error: { code: 'agent_unavailable', message: 'The agent is unavailable. Try again shortly.' } }, { status: 503 })
      }),
    )
    renderApp('/settings/agent')
    expect(await screen.findByText(/The agent is not reachable/)).toBeInTheDocument()
    expect(screen.queryByText(/No model provider is enabled/)).not.toBeInTheDocument()
    expect(screen.getByLabelText('Monthly allowance in US dollars')).toBeInTheDocument()
    expect(await screen.findByRole('table')).toBeInTheDocument()
    const stored = screen.getByRole('checkbox', { name: 'Send environment values to OpenAI' })
    expect(stored).toBeChecked()
    await userEvent.click(stored)
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(await screen.findByText('The agent is unavailable. Try again shortly.')).toBeInTheDocument()
  })

  it('formats prices from micro-dollars', async () => {
    const cheap = {
      ...SETTINGS,
      catalogue: {
        providers: [
          {
            provider: 'anthropic',
            models: [
              { id: 'model-a', input_per_mtok_micro_usd: 0, output_per_mtok_micro_usd: 70_000, context_tokens: 1 },
              { id: 'model-big', input_per_mtok_micro_usd: 1_234_567_890_000, output_per_mtok_micro_usd: 1_500_000, context_tokens: 1 },
            ],
          },
        ],
      },
    }
    useAgentMocks(cheap)
    renderApp('/settings/agent')
    expect(await screen.findByText('Input $0.00 per million tokens, output $0.07 per million tokens.')).toBeInTheDocument()
    await userEvent.selectOptions(screen.getByLabelText('Model'), 'model-big')
    expect(screen.getByText('Input $1,234,567.89 per million tokens, output $1.50 per million tokens.')).toBeInTheDocument()
  })

  it('test model shows the provider error', async () => {
    useAgentMocks()
    server.use(
      http.post('/api/agent/settings/test', () => HttpResponse.json({ ok: false, kind: 'auth', message: 'The provider rejected the key.' })),
    )
    renderApp('/settings/agent')
    expect(await screen.findByText('A test makes one small paid call.')).toBeInTheDocument()
    await userEvent.click(await screen.findByRole('button', { name: 'Test model' }))
    expect(await screen.findByText('The provider rejected the key.')).toBeInTheDocument()
  })

  it('test model shows success', async () => {
    useAgentMocks()
    server.use(http.post('/api/agent/settings/test', () => HttpResponse.json({ ok: true, kind: 'ok', message: 'The model answered.' })))
    renderApp('/settings/agent')
    await userEvent.click(await screen.findByRole('button', { name: 'Test model' }))
    expect(await screen.findByText('The model answered.')).toBeInTheDocument()
  })

  it('test model shows the budget message on 402', async () => {
    useAgentMocks()
    server.use(
      http.post('/api/agent/settings/test', () =>
        HttpResponse.json({ error: { code: 'budget_exhausted', message: 'The monthly allowance is used up.' } }, { status: 402 }),
      ),
    )
    renderApp('/settings/agent')
    await userEvent.click(await screen.findByRole('button', { name: 'Test model' }))
    expect(await screen.findByText('The monthly allowance is used up.')).toBeInTheDocument()
  })

  it('test model shows a message when the request fails', async () => {
    useAgentMocks()
    server.use(http.post('/api/agent/settings/test', () => HttpResponse.error()))
    renderApp('/settings/agent')
    await userEvent.click(await screen.findByRole('button', { name: 'Test model' }))
    expect(await screen.findByText('The test could not run. Try again.')).toBeInTheDocument()
  })

  it('ignores a test result that arrives after another model was chosen', async () => {
    useAgentMocks()
    let release!: () => void
    const gate = new Promise<void>((resolve) => (release = resolve))
    server.use(
      http.post('/api/agent/settings/test', async () => {
        await gate
        return HttpResponse.json({ ok: true, kind: 'ok', message: 'Late answer for model a.' })
      }),
    )
    renderApp('/settings/agent')
    await userEvent.click(await screen.findByRole('button', { name: 'Test model' }))
    await userEvent.selectOptions(screen.getByLabelText('Model'), 'model-b')
    release()
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(screen.queryByText('Late answer for model a.')).not.toBeInTheDocument()
  })

  it('lifting redaction asks to confirm', async () => {
    useAgentMocks()
    const bodies: unknown[] = []
    server.use(
      http.put('/api/agent/settings', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ ...SETTINGS, unredacted_providers: ['openai'] })
      }),
    )
    renderApp('/settings/agent')
    const toggle = await screen.findByRole('checkbox', { name: 'Send environment values to OpenAI' })
    expect(toggle).not.toBeChecked()
    expect(screen.getAllByText('Values that look like secrets are always hidden.').length).toBeGreaterThan(0)
    await userEvent.click(toggle)
    expect(toggle).not.toBeChecked()
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Send environment values' }))
    expect(toggle).toBeChecked()
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect((bodies[0] as { unredacted_providers: string[] }).unredacted_providers).toEqual(['openai'])
  })

  it('cancel leaves redaction off and shows stored providers that left the catalogue', async () => {
    useAgentMocks({ ...SETTINGS, unredacted_providers: ['gone'] })
    renderApp('/settings/agent')
    const toggle = await screen.findByRole('checkbox', { name: 'Send environment values to OpenAI' })
    await userEvent.click(toggle)
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(toggle).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Send environment values to gone' })).toBeChecked()
  })

  it('empty catalogue shows the operator message', async () => {
    useAgentMocks({ ...SETTINGS, catalogue: { providers: [] }, role_models: {} })
    renderApp('/settings/agent')
    expect(await screen.findByText('No model provider is enabled. Your operator sets providers up at install time.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Install guide' })).toHaveAttribute('href', 'https://github.com/rcss-labs/harbormaster/blob/main/harbormaster-docs/docs/install/ai-agent.md')
    expect(screen.queryByLabelText('Model')).not.toBeInTheDocument()
  })

  it('saves the investigator model', async () => {
    useAgentMocks()
    const bodies: unknown[] = []
    server.use(
      http.put('/api/agent/settings', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json(SETTINGS)
      }),
    )
    renderApp('/settings/agent')
    await userEvent.selectOptions(await screen.findByLabelText('Investigation provider'), 'openai')
    await userEvent.selectOptions(screen.getByLabelText('Investigation model'), 'model-c')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(bodies).toEqual([
        {
          monthly_budget_usd: '25.00',
          unredacted_providers: [],
          role_models: { chat: { provider: 'anthropic', model: 'model-a' }, investigator: { provider: 'openai', model: 'model-c' }, reviewer: null },
          version: 3,
        },
      ]),
    )
  })

  describe('the investigator key of a save', () => {
    const withInvestigator = {
      ...SETTINGS,
      role_models: { ...SETTINGS.role_models, investigator: { provider: 'openai', model: 'model-c', available: true, max_question_usd: '0.60', allowance_below_question: false } },
    }
    // The caller has set up the mocks with useAgentMocks.
    const saveAllowance = async (settings: AgentSettingsDTO, before?: () => Promise<void>) => {
      const bodies: { role_models: Record<string, unknown> }[] = []
      server.use(
        http.put('/api/agent/settings', async ({ request }) => {
          bodies.push((await request.json()) as { role_models: Record<string, unknown> })
          return HttpResponse.json(settings)
        }),
      )
      renderApp('/settings/agent')
      const allowance = await screen.findByLabelText('Monthly allowance in US dollars')
      await before?.()
      await userEvent.clear(allowance)
      await userEvent.type(allowance, '31')
      await userEvent.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(bodies).toHaveLength(1))
      return bodies[0].role_models
    }

    it('sends null when the choice is cleared', async () => {
      useAgentMocks(withInvestigator)
      const roles = await saveAllowance(withInvestigator, async () => {
        await userEvent.selectOptions(screen.getByLabelText('Investigation provider'), '')
      })
      expect(roles).toEqual({ chat: { provider: 'anthropic', model: 'model-a' }, investigator: null, reviewer: null })
    })

    it('sends the stored model unchanged when other settings are saved', async () => {
      useAgentMocks(withInvestigator)
      const roles = await saveAllowance(withInvestigator)
      expect(roles).toEqual({ chat: { provider: 'anthropic', model: 'model-a' }, investigator: { provider: 'openai', model: 'model-c' }, reviewer: null })
    })

    it('sends null when none was ever set', async () => {
      useAgentMocks(SETTINGS)
      const roles = await saveAllowance(SETTINGS)
      expect(roles).toEqual({ chat: { provider: 'anthropic', model: 'model-a' }, investigator: null, reviewer: null })
    })
  })

  describe('the model for reviews', () => {
    const withReviewer = {
      ...SETTINGS,
      role_models: { ...SETTINGS.role_models, reviewer: { provider: 'openai', model: 'model-c', available: true, max_question_usd: '0.60', allowance_below_question: false } },
    }
    const saveAllowance = async (settings: AgentSettingsDTO, before?: () => Promise<void>) => {
      const bodies: { role_models: Record<string, unknown> }[] = []
      server.use(
        http.put('/api/agent/settings', async ({ request }) => {
          bodies.push((await request.json()) as { role_models: Record<string, unknown> })
          return HttpResponse.json(settings)
        }),
      )
      renderApp('/settings/agent')
      const allowance = await screen.findByLabelText('Monthly allowance in US dollars')
      await before?.()
      await userEvent.clear(allowance)
      await userEvent.type(allowance, '31')
      await userEvent.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(bodies).toHaveLength(1))
      return bodies[0].role_models
    }

    it('saves the reviewer model', async () => {
      useAgentMocks()
      const bodies: unknown[] = []
      server.use(
        http.put('/api/agent/settings', async ({ request }) => {
          bodies.push(await request.json())
          return HttpResponse.json(SETTINGS)
        }),
      )
      renderApp('/settings/agent')
      expect(await screen.findByRole('region', { name: 'Model for reviews' })).toBeInTheDocument()
      await userEvent.selectOptions(await screen.findByLabelText('Review provider'), 'openai')
      await userEvent.selectOptions(screen.getByLabelText('Review model'), 'model-c')
      await userEvent.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() =>
        expect(bodies).toEqual([
          {
            monthly_budget_usd: '25.00',
            unredacted_providers: [],
            role_models: { chat: { provider: 'anthropic', model: 'model-a' }, investigator: null, reviewer: { provider: 'openai', model: 'model-c' } },
            version: 3,
          },
        ]),
      )
    })

    it('sends null when the choice is cleared', async () => {
      useAgentMocks(withReviewer)
      const roles = await saveAllowance(withReviewer, async () => {
        await userEvent.selectOptions(screen.getByLabelText('Review provider'), '')
      })
      expect(roles).toEqual({ chat: { provider: 'anthropic', model: 'model-a' }, investigator: null, reviewer: null })
    })

    it('sends the stored model unchanged when other settings are saved', async () => {
      useAgentMocks(withReviewer)
      const roles = await saveAllowance(withReviewer)
      expect(roles).toEqual({ chat: { provider: 'anthropic', model: 'model-a' }, investigator: null, reviewer: { provider: 'openai', model: 'model-c' } })
    })

    it('warns when the reviewer model is no longer offered', async () => {
      const reviewer = { provider: 'anthropic', model: 'retired', available: false, max_question_usd: '', allowance_below_question: false }
      useAgentMocks({ ...SETTINGS, role_models: { ...SETTINGS.role_models, reviewer } })
      renderApp('/settings/agent')
      await screen.findByLabelText('Review model')
      expect(screen.getByText('This model is no longer offered by the operator. Choose another.')).toBeInTheDocument()
    })
  })

  it('labels the reviewer role in the usage table', async () => {
    useAgentMocks()
    server.use(
      http.get('/api/agent/usage', () =>
        HttpResponse.json({
          month: '2026-11',
          days: [
            { day: '2026-11-24', role: 'reviewer', runs: 1, cost_usd: '0.30', input_tokens: 9000, output_tokens: 800 },
            { day: '2026-11-24', role: 'rollup', runs: 1, cost_usd: '0.10', input_tokens: 10, output_tokens: 10 },
          ],
        }),
      ),
    )
    renderApp('/settings/agent')
    const usage = within(await screen.findByRole('region', { name: 'Usage' }))
    expect(await usage.findByText('Reviews')).toBeInTheDocument()
    expect(usage.queryByText('reviewer')).not.toBeInTheDocument()
    // A role this version does not know is shown as it came.
    expect(usage.getByText('rollup')).toBeInTheDocument()
  })

  it('warns when the investigator model is no longer offered', async () => {
    const investigator = { provider: 'anthropic', model: 'retired', available: false, max_question_usd: '', allowance_below_question: false }
    useAgentMocks({ ...SETTINGS, role_models: { ...SETTINGS.role_models, investigator } })
    renderApp('/settings/agent')
    await screen.findByLabelText('Investigation model')
    expect(screen.getByText('This model is no longer offered by the operator. Choose another.')).toBeInTheDocument()
  })

  it('sets a tag to view and logs', async () => {
    useAgentMocks()
    const bodies: unknown[] = []
    server.use(
      http.put('/api/agent/identity', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ username: 'harbormaster-agent', permissions: [{ tag_id: 2, tag_name: 'Staging', logs: true }, { tag_id: 3, tag_name: 'Prod', logs: false }] })
      }),
    )
    renderApp('/settings/agent')
    expect(await screen.findByText('Background investigations read only what this identity can see. It cannot sign in or change anything.')).toBeInTheDocument()
    expect(screen.getByText('harbormaster-agent')).toBeInTheDocument()
    expect(await screen.findByLabelText('Access to Prod')).toHaveValue('view')
    const staging = screen.getByLabelText('Access to Staging')
    expect(staging).toHaveValue('none')
    expect(screen.getByRole('button', { name: 'Save access' })).toBeDisabled()
    await userEvent.selectOptions(staging, 'View and logs')
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }))
    await waitFor(() => expect(bodies).toEqual([{ permissions: [{ tag_id: 2, logs: true }, { tag_id: 3, logs: false }] }]))
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
  })

  it('shows the limits', async () => {
    useAgentMocks()
    renderApp('/settings/agent')
    expect(
      await screen.findByText('Alerts for the same service within 15 minutes share one investigation. At most 20 start an hour, 2 run at a time, and none waits longer than 15 minutes.'),
    ).toBeInTheDocument()
  })

  it('lists past investigations and loads more', async () => {
    useAgentMocks()
    const cursors: (string | null)[] = []
    server.use(
      http.get('/api/agent/investigations', ({ request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor')
        cursors.push(cursor)
        return HttpResponse.json(
          cursor
            ? { items: [investigation(40, { state: 'skipped', reason: 'budget', cost_usd: '', subject: { ...investigation(40).subject, service: 'auth' } })], next_cursor: '' }
            : { items: [investigation(41)], next_cursor: 'abc' },
        )
      }),
    )
    renderApp('/settings/agent')
    const link = await screen.findByRole('link', { name: 'payments-api' })
    expect(link).toHaveAttribute('href', '/investigations/41')
    const table = within(screen.getByRole('region', { name: 'Investigations' }))
    expect(table.getByText('Done')).toBeInTheDocument()
    expect(table.getByText('$0.12')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByRole('link', { name: 'auth' })).toHaveAttribute('href', '/investigations/40')
    expect(table.getByText('Skipped')).toBeInTheDocument()
    expect(table.getByText('n/a')).toBeInTheDocument()
    expect(cursors).toContain('abc')
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  })

  it('marks the cost of a retried investigation as the last attempt', async () => {
    useAgentMocks()
    server.use(http.get('/api/agent/investigations', () => HttpResponse.json({ items: [investigation(42, { attempts: 2, cost_usd: '0.30' })], next_cursor: '' })))
    renderApp('/settings/agent')
    expect(await screen.findByText('$0.30 (last attempt)')).toBeInTheDocument()
  })

  it('says when there are no investigations yet', async () => {
    useAgentMocks()
    renderApp('/settings/agent')
    expect(await screen.findByText('No investigations yet.')).toBeInTheDocument()
  })

  it('keeps the saved access when the allowance is saved afterwards', async () => {
    useAgentMocks()
    server.use(
      http.put('/api/agent/identity', () => HttpResponse.json({ username: 'harbormaster-agent', permissions: [{ tag_id: 3, tag_name: 'Prod', logs: true }] })),
      http.put('/api/agent/settings', () => HttpResponse.json({ ...SETTINGS, monthly_budget_usd: '40.00' })),
    )
    renderApp('/settings/agent')
    await userEvent.selectOptions(await screen.findByLabelText('Access to Prod'), 'View and logs')
    expect(screen.getByText('Unsaved changes', { selector: '[data-access-cue]' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }))
    await screen.findByText('Saved.')
    expect(document.querySelector('[data-access-cue]')).toBeNull()
    const allowance = screen.getByLabelText('Monthly allowance in US dollars')
    await userEvent.clear(allowance)
    await userEvent.type(allowance, '40')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.getByLabelText('Monthly allowance in US dollars')).toHaveValue('40.00'))
    expect(screen.getByLabelText('Access to Prod')).toHaveValue('logs')
  })

  it.each([
    [422, 'validation_failed', 'That tag does not exist.'],
    [403, 'forbidden', 'Only admins can change this.'],
  ])('shows an identity save error %s inline', async (status, code, message) => {
    useAgentMocks()
    server.use(http.put('/api/agent/identity', () => HttpResponse.json({ error: { code, message } }, { status })))
    renderApp('/settings/agent')
    await userEvent.selectOptions(await screen.findByLabelText('Access to Prod'), 'No access')
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(message)
    expect(screen.getByLabelText('Access to Prod')).toHaveValue('none')
  })

  it('keeps the loaded rows when Load more fails and offers Try again', async () => {
    useAgentMocks()
    let failing = true
    server.use(
      http.get('/api/agent/investigations', ({ request }) => {
        if (!new URL(request.url).searchParams.get('cursor')) return HttpResponse.json({ items: [investigation(41)], next_cursor: 'abc' })
        if (failing) return HttpResponse.json({ error: { code: 'internal', message: 'Something broke.' } }, { status: 500 })
        return HttpResponse.json({ items: [investigation(40, { subject: { ...investigation(40).subject, service: 'auth' } })], next_cursor: '' })
      }),
    )
    renderApp('/settings/agent')
    await userEvent.click(await screen.findByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('Could not load more investigations.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'payments-api' })).toBeInTheDocument()
    failing = false
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('link', { name: 'auth' })).toBeInTheDocument()
  })

  it('shows an unknown state as plain text', async () => {
    useAgentMocks()
    server.use(
      http.get('/api/agent/investigations', () =>
        HttpResponse.json({ items: [{ ...investigation(41), state: 'brand_new', reason: 'odd' }], next_cursor: '' }),
      ),
    )
    renderApp('/settings/agent')
    expect(await screen.findByText('Unknown')).toBeInTheDocument()
  })
})
