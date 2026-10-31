import { fireEvent, screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { SERVICE_NOTIFICATIONS, SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const withoutTerminal = () =>
  http.get('/api/services/:id', () =>
    HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, terminal: false } }),
  )

/** The service tab link (the sidebar and breadcrumbs reuse some of these names). */
const tabLink = (name: string | RegExp) =>
  screen.getAllByRole('link', { name }).find((link) => link.getAttribute('href')?.startsWith('/services/')) as HTMLElement

describe('ServicePage', () => {
  it('renders header, tabs and locked Terminal tab for oncall-less user', async () => {
    server.use(withoutTerminal())
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)

    expect(await screen.findByRole('heading', { name: 'payments-api', level: 1 })).toBeInTheDocument()
    expect(screen.getByText('Steady')).toBeInTheDocument()
    const clusterHrefs = screen.getAllByRole('link', { name: SERVICE_PAYMENTS.cluster.name }).map((link) => link.getAttribute('href'))
    expect(clusterHrefs).toContain(`/clusters/${SERVICE_PAYMENTS.cluster.id}`)
    expect(screen.getByText('Fargate')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Restart (new deployment)' })).toBeEnabled()
    expect(screen.getByRole('button', { name: `Desired count: ${SERVICE_PAYMENTS.desired}` })).toBeEnabled()
    expect(screen.getByRole('link', { name: 'AWS console ↗' })).toHaveAttribute('href', SERVICE_PAYMENTS.console_url)

    const terminalTab = tabLink(/Terminal/)
    expect(terminalTab).toHaveTextContent('⊘')
    expect(terminalTab).toHaveAttribute('title', `You need Terminal on ${SERVICE_PAYMENTS.tag.name}`)
    expect(tabLink(/^Logs/)).not.toHaveAttribute('title')
  })

  it('shows the lock panel instead of the placeholder on a locked tab', async () => {
    server.use(withoutTerminal())
    renderApp(`/services/${SERVICE_PAYMENTS.id}/terminal`)
    expect(await screen.findByText(/to use this/)).toBeInTheDocument()
    expect(screen.queryByText('This tab arrives in a later phase.')).not.toBeInTheDocument()
  })

  it('overview shows rollout card for notifications fixture (":9 → :10", "1 new task on :10")', async () => {
    renderApp(`/services/${SERVICE_NOTIFICATIONS.id}`)
    expect(await screen.findByText('Rollout in progress')).toBeInTheDocument()
    expect(screen.getByText(':9 → :10')).toBeInTheDocument()
    expect(screen.getByText(/1 new task on :10 · 1 old on :9 · circuit breaker is on, without rollback/)).toBeInTheDocument()
  })

  it('overview shows Completed card for payments-api with "min healthy 100% · max 200%"', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)
    expect(await screen.findByText('Completed')).toBeInTheDocument()
    expect(
      screen.getByText('Rolling update · min healthy 100% · max 200% · circuit breaker with rollback is on'),
    ).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Recent events' })).toBeInTheDocument()
  })

  it('tab route defaults to overview and switches to operations', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)
    await screen.findByText('Completed')
    expect(tabLink('Overview').className).toContain('active')

    fireEvent.click(tabLink('Operations log'))
    expect(await screen.findByRole('checkbox', { name: 'Show ECS service events' })).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/operations`)
    expect(tabLink('Operations log').className).toContain('active')
  })

  it('redirects an unknown tab to overview', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/nonsense`)
    expect(await screen.findByText('Completed')).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/overview`)
  })

  it('404 shows No access with "You need View on <tag> to open this service."', async () => {
    server.use(
      http.get('/api/services/:id', () =>
        HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 }),
      ),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)
    expect(await screen.findByRole('heading', { name: 'No access' })).toBeInTheDocument()
    expect(screen.getByText("You need View on this service's tag to open this service.")).toBeInTheDocument()
  })
})
