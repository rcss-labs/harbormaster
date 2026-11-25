import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { AgentSettingsDTO, AgentUsageDTO } from '../../../api/agent'
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
}

const USAGE: AgentUsageDTO = {
  month: '2026-11',
  days: [{ day: '2026-11-24', role: 'chat', runs: 4, cost_usd: '0.42', input_tokens: 1200, output_tokens: 340 }],
}

const useAgentMocks = (settings: AgentSettingsDTO = SETTINGS) =>
  server.use(
    http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, ai_chat: true })),
    http.get('/api/agent/settings', () => HttpResponse.json(settings)),
    http.get('/api/agent/usage', () => HttpResponse.json(USAGE)),
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
      expect(bodies).toEqual([{ monthly_budget_usd: '40', unredacted_providers: [], role_models: { chat: { provider: 'anthropic', model: 'model-b' } }, version: 3 }]),
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
      expect(bodies).toEqual([{ monthly_budget_usd: '30', unredacted_providers: [], role_models: { chat: { provider: 'anthropic', model: 'retired' } }, version: 3 }]),
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
    await waitFor(() => expect(bodies).toEqual([{ monthly_budget_usd: '12.50', unredacted_providers: [], role_models: {}, version: 3 }]))
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
      role_models: { chat: { provider: 'anthropic', model: 'model-b' } },
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
})
