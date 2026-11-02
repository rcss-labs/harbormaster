import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { MeDTO, PollerDTO } from '../../api/types'
import { ALERTS, ACCOUNT_STAGING, CLUSTERS, ME_OMER, POLLER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const originalWidth = window.innerWidth

beforeEach(() => {
  // Wide enough that the sidebar is expanded and shows its account actions inline.
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: 1440 })
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

// Let requests still in flight from an unmounted app finish, so they cannot reach the next test's handlers.
afterEach(async () => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: originalWidth })
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const nav = () => screen.findByRole('navigation', { name: 'Main' })

describe('Shell', () => {
  it('shows Admin section for admins and Account section for users', async () => {
    renderApp('/')
    const adminNav = await nav()
    expect(within(adminNav).getByText('Admin')).toBeInTheDocument()
    expect(within(adminNav).getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings/general')
    for (const gone of ['Users and permissions', 'Tags', 'AWS accounts', 'Audit log']) {
      expect(within(adminNav).queryByRole('link', { name: new RegExp(gone) })).not.toBeInTheDocument()
    }
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

  it('clearing a restored filter to All stays All', async () => {
    localStorage.setItem('harbormaster-filters:pavel', JSON.stringify({ account: ACCOUNT_STAGING.account_id }))
    renderApp('/clusters')
    const user = userEvent.setup()
    const account = await screen.findByLabelText('Account filter')
    await waitFor(() => expect(account).toHaveValue(ACCOUNT_STAGING.account_id))
    await user.selectOptions(account, '')
    await waitFor(() => expect(account).toHaveValue(''))
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(account).toHaveValue('')
    expect(screen.getByTestId('search')).not.toHaveTextContent('account=')
  })

  it('a filter chosen in-session survives a sidebar link', async () => {
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.selectOptions(await screen.findByLabelText('Account filter'), ACCOUNT_STAGING.account_id)
    await user.click(await screen.findByRole('link', { name: /Tasks/ }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/tasks'))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(`account=${ACCOUNT_STAGING.account_id}`))
    expect(screen.getByLabelText('Account filter')).toHaveValue(ACCOUNT_STAGING.account_id)
  })

  it('tag filter writes repeated tag= params and localStorage', async () => {
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Tag filter' }))
    await user.click(screen.getByRole('checkbox', { name: 'Prod' }))
    await user.click(screen.getByRole('checkbox', { name: 'Staging' }))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('tag=2'))
    expect(screen.getByTestId('search')).toHaveTextContent('tag=3')
    expect(JSON.parse(localStorage.getItem('harbormaster-filters:pavel')!)).toEqual({ tag: [2, 3] })
  })

  it('URL tag= wins over the stored tags', async () => {
    localStorage.setItem('harbormaster-filters:pavel', JSON.stringify({ tag: [2] }))
    renderApp('/clusters?tag=3')
    expect(await screen.findByRole('button', { name: 'Tag filter' })).toHaveTextContent('Prod')
    expect(screen.getByRole('button', { name: 'Tag filter' })).not.toHaveTextContent('Staging')
  })

  it('drops stored tag ids the user can no longer see', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    localStorage.setItem('harbormaster-filters:omer', JSON.stringify({ tag: [3, 2] }))
    const tagParams: string[][] = []
    server.use(
      http.get('/api/clusters', ({ request }) => {
        tagParams.push(new URL(request.url).searchParams.getAll('tag'))
        return HttpResponse.json(CLUSTERS)
      }),
    )
    renderApp('/clusters')
    expect(await screen.findByRole('button', { name: 'Tag filter' })).toHaveTextContent('Staging')
    await waitFor(() => expect(tagParams.length).toBeGreaterThan(0))
    expect(tagParams.every((tags) => tags.length === 1 && tags[0] === '2')).toBe(true)
  })

  it('restores stored tags sorted ascending into the URL', async () => {
    localStorage.setItem('harbormaster-filters:pavel', JSON.stringify({ tag: [3, 2] }))
    renderApp('/clusters')
    await waitFor(() => expect(screen.getByTestId('search').textContent).toBe('tag=2&tag=3'))
  })

  it('non-admins only see tags they can View in the tag filter options', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/clusters')
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Tag filter' }))
    expect(screen.getByRole('checkbox', { name: 'Dev' })).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Staging' })).toBeInTheDocument()
    expect(screen.queryByRole('checkbox', { name: 'Prod' })).not.toBeInTheDocument()
  })

  it('a tag filter chosen in-session survives a sidebar link', async () => {
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Tag filter' }))
    await user.click(screen.getByRole('checkbox', { name: 'Prod' }))
    await user.click(await screen.findByRole('link', { name: /Tasks/ }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/tasks'))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('tag=3'))
    expect(screen.getByRole('button', { name: 'Tag filter' })).toHaveTextContent('Prod')
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
    await user.click(await screen.findByRole('button', { name: /Search or run a command/ }))
    const palette = await screen.findByRole('dialog', { name: 'Command palette' })
    await user.type(within(palette).getByRole('combobox'), cluster.name)
    const option = await within(palette).findByRole('option', { name: new RegExp(cluster.name) })
    expect(option).toBeInTheDocument()
    await waitFor(() => expect(within(palette).getAllByRole('option')[0]).toHaveTextContent(cluster.name))
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/clusters/${cluster.id}`))
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument()
  })
})

describe('Search filters', () => {
  it('search sends the current filters', async () => {
    const urls: URL[] = []
    server.use(
      http.get('/api/search', ({ request }) => {
        urls.push(new URL(request.url))
        return HttpResponse.json({ items: [] })
      }),
    )
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.selectOptions(await screen.findByLabelText('Account filter'), ACCOUNT_STAGING.account_id)
    await user.click(screen.getByRole('button', { name: 'Tag filter' }))
    await user.click(screen.getByRole('checkbox', { name: 'Prod' }))
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: /Search or run a command/ }))
    await user.type(within(await screen.findByRole('dialog', { name: 'Command palette' })).getByRole('combobox'), 'pa')
    await waitFor(() => expect(urls.length).toBeGreaterThan(0))
    const last = urls[urls.length - 1]
    expect(last.searchParams.get('account')).toBe(ACCOUNT_STAGING.account_id)
    expect(last.searchParams.getAll('tag')).toEqual(['3'])
  })
})

describe('Sidebar', () => {
  it('Overview badge shows the danger count, hidden at zero', async () => {
    const alerts = (danger: number) => http.get('/api/alerts', () => HttpResponse.json({ ...ALERTS, counts: { danger, warning: 0, info: 0 } }))
    server.use(alerts(1))
    const view = renderApp('/')
    const overviewLink = await within(await nav()).findByRole('link', { name: /Overview/ })
    const badge = await within(overviewLink).findByText('1')
    expect(badge.className).toMatch(/countBad/)
    view.unmount()

    server.use(alerts(0))
    renderApp('/')
    const link = await within(await nav()).findByRole('link', { name: /Overview/ })
    await waitFor(() => expect(link).toHaveTextContent(/^OVOverview$/))
  })

  it('Networks link is enabled and points to /networks', async () => {
    renderApp('/')
    const link = within(await nav()).getByRole('link', { name: 'Networks' })
    expect(link).toHaveAttribute('href', '/networks')
    expect(link).not.toHaveAttribute('aria-disabled')
  })

  it('Storage link is enabled and points to /storage', async () => {
    renderApp('/')
    const link = within(await nav()).getByRole('link', { name: 'Storage' })
    expect(link).toHaveAttribute('href', '/storage')
    expect(link).not.toHaveAttribute('aria-disabled')
  })
})
