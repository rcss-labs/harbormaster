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

  it('shows a stale model warning', async () => {
    useAgentMocks({ ...SETTINGS, role_models: { chat: { provider: 'anthropic', model: 'retired', available: false } } })
    renderApp('/settings/agent')
    expect(await screen.findByText('This model is no longer offered by the operator. Choose another.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('test model shows the provider error', async () => {
    useAgentMocks()
    server.use(
      http.post('/api/agent/settings/test', () => HttpResponse.json({ ok: false, kind: 'auth', message: 'The provider rejected the key.' })),
    )
    renderApp('/settings/agent')
    await userEvent.click(await screen.findByRole('button', { name: 'Test model' }))
    expect(await screen.findByText('The provider rejected the key.')).toBeInTheDocument()
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

  it('empty catalogue shows the operator message', async () => {
    useAgentMocks({ ...SETTINGS, catalogue: { providers: [] }, role_models: {} })
    renderApp('/settings/agent')
    expect(await screen.findByText('No model provider is enabled. Your operator sets providers up at install time.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Install guide' })).toHaveAttribute('href', expect.stringContaining('install'))
    expect(screen.queryByLabelText('Model')).not.toBeInTheDocument()
  })
})
