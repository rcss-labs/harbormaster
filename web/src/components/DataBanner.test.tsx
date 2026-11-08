import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { POLLER_TIERS } from '../test/fixtures'
import { server } from '../test/msw'
import { DataBanner } from './DataBanner'

const renderWithProviders = (ui: ReactNode) =>
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>)

describe('DataBanner', () => {
  it('names the stale data this page depends on', async () => {
    server.use(http.get('/api/poller', () => HttpResponse.json(POLLER_TIERS)))
    renderWithProviders(<DataBanner tiers={['Auto scaling']} />)
    expect(await screen.findByText('Some data on this page is stale: Auto scaling')).toBeInTheDocument()
  })

  it('stays silent when the failing data is not used by this page', async () => {
    let served = false
    server.use(http.get('/api/poller', () => { served = true; return HttpResponse.json(POLLER_TIERS) }))
    const { container } = renderWithProviders(<DataBanner tiers={['Tasks']} />)
    await waitFor(() => expect(served).toBe(true))
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(container.querySelector('[data-hm-banner]')).toBeNull()
  })
})
