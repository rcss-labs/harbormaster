import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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

const withCan = (can: Partial<typeof SERVICE_PAYMENTS.can>) =>
  http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, ...can } }))
const viewerOnly = { logs: false, operate: false, deploy: false, edit: false, terminal: false }

describe('ServicePage', () => {
  it('Deploy tab is bold and unlocked for Operate-only', async () => {
    server.use(withCan({ ...viewerOnly, operate: true }))
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)
    await screen.findByRole('heading', { name: 'payments-api', level: 1 })
    const deploy = tabLink(/Deploy/)
    expect(deploy.querySelector('strong')).toHaveTextContent('Deploy')
    expect(deploy).not.toHaveAttribute('title')
    expect(deploy).not.toHaveTextContent('⊘')
    expect(tabLink('Overview').querySelector('strong')).toBeNull()
    expect(tabLink(/Task definition/).querySelector('strong')).toBeNull()
  })

  it('Deploy tab unlocked for Edit-only', async () => {
    server.use(withCan({ ...viewerOnly, edit: true }))
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)
    await screen.findByRole('heading', { name: 'payments-api', level: 1 })
    expect(tabLink(/Deploy/)).not.toHaveAttribute('title')
    expect(tabLink(/Deploy/)).not.toHaveTextContent('⊘')
  })

  it('Deploy tab locked for Viewer', async () => {
    server.use(withCan(viewerOnly))
    renderApp(`/services/${SERVICE_PAYMENTS.id}/deploy`)
    await screen.findByRole('heading', { name: 'payments-api', level: 1 })
    const deploy = tabLink(/Deploy/)
    expect(deploy).toHaveTextContent('⊘')
    expect(deploy).toHaveAttribute('title', `You need Edit, Deploy or Operate on ${SERVICE_PAYMENTS.tag.name}`)
    expect(await screen.findByText(/to use this/)).toBeInTheDocument()
    expect(screen.getByText('Edit, Deploy or Operate')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Containers' })).not.toBeInTheDocument()
  })

  it('renders header, tabs and locked Terminal tab for oncall-less user', async () => {
    server.use(withoutTerminal())
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)

    expect(await screen.findByRole('heading', { name: 'payments-api', level: 1 })).toBeInTheDocument()
    expect(screen.getByText('Steady')).toBeInTheDocument()
    const clusterHrefs = screen.getAllByRole('link', { name: SERVICE_PAYMENTS.cluster.name }).map((link) => link.getAttribute('href'))
    expect(clusterHrefs).toContain(`/clusters/${SERVICE_PAYMENTS.cluster.id}`)
    expect(screen.getByText('Fargate')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Restart (new deployment)' })).toBeEnabled()
    expect(screen.getByRole('spinbutton', { name: 'Desired count' })).toHaveValue(SERVICE_PAYMENTS.desired)
    expect(screen.getByRole('spinbutton', { name: 'Desired count' })).toBeEnabled()
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

  it('Networking card links to the VPC network tree and names the security groups', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)
    await screen.findByText('Completed')
    const { vpc, security_group_names: securityGroupNames } = SERVICE_PAYMENTS.networking
    const treeLink = screen.getByRole('link', { name: 'Network placement →' })
    expect(treeLink).toHaveAttribute('href', `/networks/${SERVICE_PAYMENTS.account_id}/${SERVICE_PAYMENTS.region}/${vpc!.vpc_id}`)
    for (const [groupId, groupName] of Object.entries(securityGroupNames)) {
      expect(screen.getByText(`${groupId} (${groupName})`)).toBeInTheDocument()
    }
  })

  it('omits the Network placement link when the service has no VPC', async () => {
    server.use(
      http.get('/api/services/:id', () =>
        HttpResponse.json({ ...SERVICE_PAYMENTS, networking: { ...SERVICE_PAYMENTS.networking, vpc: null } }),
      ),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}`)
    await screen.findByText('Completed')
    expect(screen.queryByRole('link', { name: 'Network placement →' })).not.toBeInTheDocument()
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

  it('pending banner navigates to the Deploy tab', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/overview`)
    await userEvent.click(await screen.findByRole('button', { name: 'Deploy :43' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/deploy`))
    expect(screen.getByTestId('search')).toHaveTextContent('revision=43')
  })
})
