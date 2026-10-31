import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { OVERVIEW } from '../../test/fixtures'
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
})
