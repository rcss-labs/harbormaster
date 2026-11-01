import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { ALERTS, ME_OMER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const rows = async () => {
  const heading = await screen.findByRole('heading', { name: 'Needs attention' })
  const card = heading.closest('div')?.parentElement as HTMLElement
  await within(card).findByText(ALERTS.items[0].title)
  return { card, rows: Array.from(card.querySelectorAll('[data-testid="alert-row"]')) as HTMLElement[] }
}

describe('NeedsAttention', () => {
  it('renders rows worst first with pill text', async () => {
    renderApp('/')
    const { card, rows: alertRows } = await rows()
    expect(alertRows).toHaveLength(4)
    expect(within(alertRows[0]).getByText('Failing')).toBeInTheDocument()
    expect(within(alertRows[2]).getByText('Warning')).toBeInTheDocument()
    expect(within(alertRows[3]).getByText('In progress')).toBeInTheDocument()
    expect(alertRows[0]).toHaveAttribute('href', `/services/${ALERTS.items[0].service_id}/tasks`)
    expect(alertRows[3]).toHaveAttribute('href', `/services/${ALERTS.items[3].service_id}/overview`)
    expect(within(card).getByText('4 open')).toBeInTheDocument()
  })

  it('links account alerts to /settings/accounts for admins', async () => {
    renderApp('/')
    const { rows: alertRows } = await rows()
    expect(alertRows[1]).toHaveAttribute('href', '/settings/accounts')
  })

  it('empty state text', async () => {
    server.use(http.get('/api/alerts', () => HttpResponse.json({ items: [], counts: { danger: 0, warning: 0, info: 0 }, polled_at: null })))
    renderApp('/')
    expect(await screen.findByText('Nothing needs attention.')).toBeInTheDocument()
    expect(screen.getByText('0 open')).toBeInTheDocument()
  })
})

describe('NeedsAttention for non-admins', () => {
  it('does not link account alerts', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/')
    const { rows: alertRows } = await rows()
    expect(alertRows[1]).not.toHaveAttribute('href')
  })
})
