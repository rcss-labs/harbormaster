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
  monthly_budget_usd: '25.00',
  unredacted_providers: [],
  role_models: { chat: { provider: 'anthropic', model: 'model-a', available: true } },
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
      expect(bodies).toEqual([{ monthly_budget_usd: '40', unredacted_providers: [], role_models: { chat: { provider: 'anthropic', model: 'model-b' } } }]),
    )
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
  })

  it('shows a stale model warning and still saves other settings with it unchanged', async () => {
    const stale = { ...SETTINGS, role_models: { chat: { provider: 'anthropic', model: 'retired', available: false } } }
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
      expect(bodies).toEqual([{ monthly_budget_usd: '30', unredacted_providers: [], role_models: { chat: { provider: 'anthropic', model: 'retired' } } }]),
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
    await waitFor(() => expect(bodies).toEqual([{ monthly_budget_usd: '12.50', unredacted_providers: [], role_models: {} }]))
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
