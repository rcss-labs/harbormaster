import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { MeDTO, PollerDTO } from '../../api/types'
import { ACCOUNT_STAGING, CLUSTERS, ME_OMER, POLLER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const nav = () => screen.findByRole('navigation', { name: 'Main' })

describe('Shell', () => {
  it('shows Admin section for admins and Account section for users', async () => {
    renderApp('/')
    const adminNav = await nav()
    expect(within(adminNav).getByText('Admin')).toBeInTheDocument()
    expect(within(adminNav).getByRole('link', { name: /Users and permissions/ })).toHaveAttribute('href', '/admin/users')
    expect(within(adminNav).getByRole('link', { name: /Audit log/ })).toBeInTheDocument()
    expect(within(adminNav).queryByText('My access')).not.toBeInTheDocument()
  })

  it('shows the Account section with My access for non-admins', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/')
    const userNav = await nav()
    expect(within(userNav).getByText('Account')).toBeInTheDocument()
    expect(within(userNav).getByRole('link', { name: 'My access' })).toHaveAttribute('href', '/me/access')
    expect(within(userNav).queryByText('Admin')).not.toBeInTheDocument()
    expect(screen.getByText(/Signed in as/)).toHaveTextContent('omer · User')
  })

  it('sign out calls /api/auth/logout and returns to /login', async () => {
    let logoutCalls = 0
    server.use(
      http.post('/api/auth/logout', () => {
        logoutCalls += 1
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/')
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/login'))
    expect(logoutCalls).toBe(1)
  })

  it('still returns to /login when the logout request fails', async () => {
    server.use(http.post('/api/auth/logout', () => HttpResponse.json({ error: { code: 'internal', message: 'boom' } }, { status: 500 })))
    renderApp('/')
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/login'))
  })

  it('filter change updates the URL and localStorage', async () => {
    renderApp('/clusters?keep=1')
    const user = userEvent.setup()
    const account = await screen.findByLabelText('Account filter')
    await user.selectOptions(account, ACCOUNT_STAGING.account_id)
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(`account=${ACCOUNT_STAGING.account_id}`))
    expect(screen.getByTestId('search')).toHaveTextContent('keep=1')
    expect(JSON.parse(localStorage.getItem('harbormaster-filters:pavel')!)).toEqual({ account: ACCOUNT_STAGING.account_id })
  })

  it('falls back to All when the stored account no longer exists', async () => {
    localStorage.setItem('harbormaster-filters:pavel', JSON.stringify({ account: '999999999999', region: 'us-east-1' }))
    renderApp('/clusters')
    const account = await screen.findByLabelText('Account filter')
    expect(account).toHaveValue('')
    expect(screen.getByLabelText('Region filter')).toHaveValue('us-east-1')
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('region=us-east-1'))
    expect(screen.getByTestId('search')).not.toHaveTextContent('account=')
  })

  it('renders throttled and stale banners from poller status', async () => {
    const [base] = POLLER.items
    const poller: PollerDTO = {
      items: [
        { ...base, account_name: 'homelab', region: 'eu-west-1', failing: true, last_error: 'AccessDenied', last_ok_at: new Date(Date.now() - 7.5 * 60_000).toISOString() },
        { ...base, account_name: 'staging', region: 'us-east-1', throttled: true },
      ],
    }
    server.use(http.get('/api/poller', () => HttpResponse.json(poller)))
    renderApp('/')
    expect(await screen.findByText(/is 7 min old: AccessDenied/)).toHaveTextContent('Data for homelab / eu-west-1 is 7 min old: AccessDenied')
    expect(screen.getByText(/are slowed by AWS throttling/)).toHaveTextContent('Updates for staging / us-east-1 are slowed by AWS throttling')
  })

  it('shows the no-access message for a user without permissions', async () => {
    const noPerms: MeDTO = { ...ME_OMER, perms: {} }
    server.use(http.get('/api/me', () => HttpResponse.json(noPerms)))
    renderApp('/clusters')
    expect(await screen.findByText("You don't have access to anything yet. Ask a global admin.")).toBeInTheDocument()
    expect(screen.queryByText('Page not found.')).not.toBeInTheDocument()
  })

  it('toggles the theme and remembers it', async () => {
    document.documentElement.dataset.theme = 'light'
    renderApp('/')
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Toggle theme' }))
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('harbormaster-theme')).toBe('dark')
    delete document.documentElement.dataset.theme
  })
})

describe('Search', () => {
  it('lists results and navigates on Enter', async () => {
    const cluster = CLUSTERS.items[0]
    renderApp('/')
    const user = userEvent.setup()
    await user.type(await screen.findByPlaceholderText('Jump to cluster, service, task'), cluster.name)
    const option = await screen.findByRole('link', { name: new RegExp(cluster.name) })
    expect(option).toHaveTextContent('cluster')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/clusters/${cluster.id}`))
    expect(screen.queryByRole('link', { name: new RegExp(cluster.name) })).not.toBeInTheDocument()
  })
})
