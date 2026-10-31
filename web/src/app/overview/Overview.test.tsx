import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { OVERVIEW } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

/** The tile whose label (k) text matches; the label is a div, so the nav links are not matched. */
const tileLabelled = async (label: string): Promise<HTMLElement> => {
  const labelElement = await screen.findByText(label, { selector: 'div' })
  return labelElement.parentElement as HTMLElement
}

describe('Overview', () => {
  it('renders tiles from fixture (6 clusters, "3 healthy", 15 services, "2 need attention")', async () => {
    renderApp('/')
    const clustersTile = await tileLabelled('Clusters')
    expect(within(clustersTile).getByText('6', { exact: true })).toBeInTheDocument()
    expect(within(clustersTile).getByText('3 healthy')).toBeInTheDocument()

    const servicesTile = await tileLabelled('Services')
    expect(within(servicesTile).getByText('15', { exact: true })).toBeInTheDocument()
    expect(within(servicesTile).getByText('2 need attention')).toBeInTheDocument()
  })

  it('task health donut legend shows Missing count', async () => {
    renderApp('/')
    const missingRow = (await screen.findByText('Missing (desired − running)')).parentElement as HTMLElement
    expect(within(missingRow).getByText(String(OVERVIEW.task_health.missing), { exact: true })).toBeInTheDocument()
  })

  it('live events link to the service operations tab', async () => {
    renderApp('/')
    const event = OVERVIEW.events[0]
    const serviceLinks = await screen.findAllByRole('link', { name: event.service.name })
    expect(serviceLinks.length).toBeGreaterThan(0)
    for (const link of serviceLinks) {
      expect(link).toHaveAttribute('href', `/services/${event.service.id}/operations`)
    }
  })

  it('shows "last poll" from polled_at', async () => {
    renderApp('/')
    const meta = await screen.findByText(/last poll/)
    expect(within(meta).getByTitle(new Date(OVERVIEW.polled_at).toLocaleString())).toBeInTheDocument()
  })

  it('says "no data yet" before the first poll (zero time)', async () => {
    server.use(http.get('/api/overview', () => HttpResponse.json({ ...OVERVIEW, polled_at: '0001-01-01T00:00:00Z' })))
    renderApp('/')
    const meta = await screen.findByText(/last poll/)
    expect(within(meta).getByText('no data yet')).toBeInTheDocument()
  })

  it('an event without a service links to its cluster instead of a broken service URL', async () => {
    const cluster = OVERVIEW.events[0].cluster
    const clusterEvent = {
      ...OVERVIEW.events[0],
      service: { id: '', name: '', status: '' },
      cluster,
      text: 'Edited the cluster tag.',
    }
    server.use(http.get('/api/overview', () => HttpResponse.json({ ...OVERVIEW, events: [clusterEvent] })))
    renderApp('/')
    const row = (await screen.findByText('Edited the cluster tag.')).parentElement as HTMLElement
    const link = within(row).getByRole('link', { name: cluster.name })
    expect(link).toHaveAttribute('href', `/clusters/${cluster.id}`)
  })
})
