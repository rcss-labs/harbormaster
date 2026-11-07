import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { MeDTO, PollerDTO } from '../../api/types'
import { POLLER, POLLER_TIERS, ME_ADMIN, ME_OMER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { AuthProvider } from '../auth/AuthProvider'
import { FreshnessControl } from './FreshnessControl'

function renderControl(poller: PollerDTO, me: MeDTO = ME_ADMIN) {
  server.use(
    http.get('/api/poller', () => HttpResponse.json(poller)),
    http.get('/api/me', () => HttpResponse.json(me)),
  )
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <AuthProvider>
          <FreshnessControl />
        </AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const openControl = async (pattern: RegExp) => {
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: pattern }))
  return screen.getByRole('dialog', { name: 'Data freshness' })
}

/** The POLLER fixture's first region, with the Auto scaling tier failing for 4 minutes. */
const withStaleScaling = (): PollerDTO => ({
  items: POLLER.items.map((item, index) =>
    index === 0
      ? {
          ...item,
          tiers: item.tiers.map((tier) =>
            tier.label === 'Auto scaling'
              ? { ...tier, failing: true, last_ok_at: new Date(Date.now() - 4 * 60_000).toISOString(), last_error: 'DescribeScheduledActions: UnsupportedOperation' }
              : tier,
          ),
        }
      : item,
  ),
})

describe('FreshnessControl', () => {
  it('reads all current on a healthy estate', async () => {
    renderControl(POLLER)
    const count = POLLER.items.length
    expect(await screen.findByRole('button', { name: `${count} of ${count} regions current` })).toBeInTheDocument()
  })

  it('2 of 3 regions current from the fixture', async () => {
    const [first, second, third] = POLLER.items
    const poller: PollerDTO = {
      items: [
        first,
        second,
        { ...third, tiers: third.tiers.map((tier) => (tier.label === 'Tasks' ? { ...tier, failing: true, last_error: 'boom' } : tier)) },
      ],
    }
    renderControl(poller)
    expect(await screen.findByRole('button', { name: '2 of 3 regions current' })).toBeInTheDocument()
  })

  it('names only the failed data type', async () => {
    renderControl(POLLER_TIERS)
    const dialog = await openControl(/^1 of 1 regions? current$/)
    expect(within(dialog).getByText(/Auto scaling is \d+ min old|Auto scaling is unavailable/)).toBeInTheDocument()
    expect(within(dialog).queryByText(/Clusters and services is/)).not.toBeInTheDocument()
    expect(within(dialog).queryByText(/Tasks is/)).not.toBeInTheDocument()
    expect(within(dialog).getByText(/Current: .*Clusters and services/)).toBeInTheDocument()
  })

  it('counts a region current when only a non-core tier fails, and names the failure beside the count', async () => {
    renderControl(POLLER_TIERS)
    expect(await screen.findByRole('button', { name: '1 of 1 regions current' })).toBeInTheDocument()
    expect(screen.getByText('auto scaling stale in 1 region')).toBeInTheDocument()
  })

  it('counts a region as not current when a core tier fails', async () => {
    const [first] = POLLER_TIERS.items
    renderControl({ items: [{ ...first, tiers: first.tiers.map((tier) => (tier.core ? { ...tier, failing: true, last_error: 'boom' } : tier)) }] })
    expect(await screen.findByRole('button', { name: '0 of 1 regions current' })).toBeInTheDocument()
  })

  it('states the age of the stale data type', async () => {
    renderControl(withStaleScaling())
    const dialog = await openControl(/regions current/)
    expect(within(dialog).getByText('Auto scaling is 4 min old')).toBeInTheDocument()
  })

  it('keeps the raw AWS error behind Show AWS error', async () => {
    renderControl(withStaleScaling())
    const dialog = await openControl(/regions current/)
    expect(within(dialog).queryByText(/UnsupportedOperation/)).not.toBeInTheDocument()
    await userEvent.setup().click(within(dialog).getByRole('button', { name: 'Show AWS error' }))
    expect(within(dialog).getByText(/UnsupportedOperation/)).toBeInTheDocument()
  })

  it('offers Open AWS accounts to admins only', async () => {
    const view = renderControl(withStaleScaling())
    const dialog = await openControl(/regions current/)
    expect(within(dialog).getByRole('link', { name: 'Open AWS accounts' })).toHaveAttribute('href', '/settings/accounts')
    view.unmount()

    renderControl(withStaleScaling(), ME_OMER)
    const userDialog = await openControl(/regions current/)
    expect(within(userDialog).queryByRole('link', { name: 'Open AWS accounts' })).not.toBeInTheDocument()
  })

  it('non-admin sees no raw error for current tiers', async () => {
    renderControl(POLLER, ME_OMER)
    const count = POLLER.items.length
    const dialog = await openControl(new RegExp(`${count} of ${count} regions current`))
    expect(within(dialog).queryByRole('button', { name: 'Show AWS error' })).not.toBeInTheDocument()
    expect(within(dialog).queryByRole('link', { name: 'Open AWS accounts' })).not.toBeInTheDocument()
  })
})
