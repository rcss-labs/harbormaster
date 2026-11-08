import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MeDTO } from '../../api/types'
import { AUTH_OPTIONS_ON, ME_ADMIN, ME_SSO_USER, SERVICE_PAYMENTS, TASKS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import { navigateExternal } from './navigateExternal'
import { openAccountMenu } from './testUtils'

vi.mock('./navigateExternal', () => ({ navigateExternal: vi.fn() }))

const originalWidth = window.innerWidth

beforeEach(() => {
  // Wide enough that the sidebar is expanded and shows its account actions inline.
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: 1440 })
  localStorage.clear()
  window.history.replaceState(null, '', '/')
  vi.mocked(navigateExternal).mockClear()
})

afterEach(async () => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: originalWidth })
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const ssoOn = () => server.use(http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)))
const meIs = (me: MeDTO) => server.use(http.get('/api/me', () => HttpResponse.json(me)))
const LINK = 'Link SSO account'
const nav = () => screen.findByRole('navigation', { name: 'Main' })

describe('Sidebar SSO linking', () => {
  it('hides Change password for password-less users', async () => {
    meIs(ME_SSO_USER)
    renderApp('/')
    const menu = within(await openAccountMenu(userEvent.setup()))
    expect(menu.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
    expect(menu.queryByRole('button', { name: 'Change password' })).not.toBeInTheDocument()
  })

  it('shows Link SSO account only when SSO is configured, the user has a password and is not linked', async () => {
    // SSO not configured (default handler): hidden.
    const first = renderApp('/')
    await openAccountMenu(userEvent.setup())
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('button', { name: LINK })).not.toBeInTheDocument()
    first.unmount()

    // Configured, password, not linked: shown.
    ssoOn()
    const second = renderApp('/')
    await openAccountMenu(userEvent.setup())
    expect(await screen.findByRole('button', { name: LINK })).toBeInTheDocument()
    second.unmount()

    // Configured but already linked: hidden.
    meIs({ ...ME_ADMIN, sso_linked: true })
    renderApp('/')
    await openAccountMenu(userEvent.setup())
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('button', { name: LINK })).not.toBeInTheDocument()
  })

  it('dialog posts the password and navigates to redirect_url', async () => {
    ssoOn()
    let body: unknown
    server.use(
      http.post('/api/auth/saml/link', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ redirect_url: 'https://idp.example/sso?x' })
      }),
    )
    renderApp('/')
    const user = userEvent.setup()
    await openAccountMenu(user)
    await user.click(await screen.findByRole('button', { name: LINK }))
    const dialog = screen.getByRole('dialog', { name: 'Link your SSO account' })
    await user.type(within(dialog).getByLabelText('Password'), 'hunter2')
    await user.click(within(dialog).getByRole('button', { name: 'Continue to identity provider' }))
    await waitFor(() => expect(navigateExternal).toHaveBeenCalledWith('https://idp.example/sso?x'))
    expect(body).toEqual({ password: 'hunter2' })
  })

  it('refuses to navigate to a non-http(s) redirect_url and shows a generic error', async () => {
    ssoOn()
    server.use(http.post('/api/auth/saml/link', () => HttpResponse.json({ redirect_url: 'javascript:alert(1)' })))
    renderApp('/')
    const user = userEvent.setup()
    await openAccountMenu(user)
    await user.click(await screen.findByRole('button', { name: LINK }))
    const dialog = screen.getByRole('dialog', { name: 'Link your SSO account' })
    await user.type(within(dialog).getByLabelText('Password'), 'hunter2')
    await user.click(within(dialog).getByRole('button', { name: 'Continue to identity provider' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(/.+/)
    expect(navigateExternal).not.toHaveBeenCalled()
  })

  it('dialog shows the API error for a wrong password', async () => {
    ssoOn()
    server.use(
      http.post('/api/auth/saml/link', () =>
        HttpResponse.json({ error: { code: 'unauthenticated', message: 'Current password is wrong.' } }, { status: 401 }),
      ),
    )
    renderApp('/')
    const user = userEvent.setup()
    await openAccountMenu(user)
    await user.click(await screen.findByRole('button', { name: LINK }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText('Password'), 'nope')
    await user.click(within(dialog).getByRole('button', { name: 'Continue to identity provider' }))
    expect(await within(dialog).findByText('Current password is wrong.')).toBeInTheDocument()
    expect(navigateExternal).not.toHaveBeenCalled()
  })
})

describe('Shell sso_link toasts', () => {
  it('sso_link=ok shows the toast, refreshes /api/me and strips the param', async () => {
    let meCalls = 0
    server.use(
      http.get('/api/me', () => {
        meCalls += 1
        return HttpResponse.json(ME_ADMIN)
      }),
    )
    renderApp('/?sso_link=ok')
    expect(await screen.findByText('Your SSO account is linked.')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
    await waitFor(() => expect(meCalls).toBeGreaterThanOrEqual(2))
  })

  it('sso_link=sso_identity_taken shows the taken message', async () => {
    renderApp('/?sso_link=sso_identity_taken')
    expect(await screen.findByText('That SSO identity is already linked to another user.')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
  })

  it('any other sso_link value shows the generic message', async () => {
    renderApp('/?sso_link=link_user_gone')
    expect(await screen.findByText('Linking failed. Ask a global admin to check the audit log.')).toBeInTheDocument()
  })
})

describe('Sidebar Tasks badges', () => {
  const tasksWith = (unhealthy: number, stoppedError: number) =>
    server.use(
      http.get('/api/tasks', () =>
        HttpResponse.json({
          ...TASKS,
          counts: { ...TASKS.counts, stopped_error: stoppedError },
          summary: { ...TASKS.summary, by_status: { ...TASKS.summary.by_status, running_unhealthy: unhealthy } },
        }),
      ),
    )
  const tasksLink = async () => {
    const link = await screen.findByRole('link', { name: /^Tasks/ })
    // Let the tasks query resolve before reading the link.
    await new Promise((resolve) => setTimeout(resolve, 100))
    return link
  }

  it('shows no number when nothing is unhealthy or stopped with errors, not even the running total', async () => {
    tasksWith(0, 0)
    renderApp('/')
    expect(await tasksLink()).toHaveTextContent(/^Tasks$/)
  })

  it('does not count expected stops (stopped_expected) in the badge', async () => {
    server.use(
      http.get('/api/tasks', () =>
        HttpResponse.json({
          ...TASKS,
          counts: { ...TASKS.counts, stopped_error: 0 },
          summary: { ...TASKS.summary, by_status: { ...TASKS.summary.by_status, running_unhealthy: 0, stopped_error: 0, stopped_expected: 4 } },
        }),
      ),
    )
    renderApp('/')
    const link = await tasksLink()
    expect(link).toHaveTextContent(/^Tasks$/)
    expect(within(link).queryByLabelText(/stopped with errors/)).not.toBeInTheDocument()
  })

  it('shows only the yellow unhealthy count', async () => {
    tasksWith(3, 0)
    renderApp('/')
    const link = await tasksLink()
    await waitFor(() => expect(link).toHaveTextContent(/^Tasks3$/))
    expect(within(link).getByLabelText('3 unhealthy')).toBeInTheDocument()
    expect(within(link).queryByLabelText(/stopped with errors/)).not.toBeInTheDocument()
  })

  it('shows only the red stopped-with-errors count, using the stopped-tasks window', async () => {
    tasksWith(0, 2)
    renderApp('/')
    const link = await tasksLink()
    await waitFor(() => expect(link).toHaveTextContent(/^Tasks2$/))
    expect(within(link).getByLabelText(/^2 stopped with errors in the last \d+ min$/)).toBeInTheDocument()
    expect(within(link).queryByLabelText(/unhealthy/)).not.toBeInTheDocument()
  })

  it('shows both, yellow first', async () => {
    tasksWith(3, 2)
    renderApp('/')
    const link = await tasksLink()
    await waitFor(() => expect(link).toHaveTextContent(/^Tasks32$/))
  })
})

describe('Sidebar Analytics', () => {
  it('shows Security under Insights, after Analytics and without a count', async () => {
    renderApp('/')
    const link = await screen.findByRole('link', { name: /^Security/ })
    expect(link).toHaveTextContent(/^Security$/)
    expect(link.querySelector('svg')).not.toBeNull()
    const names = within(screen.getByRole('navigation', { name: 'Main' }))
      .getAllByRole('link')
      .map((item) => item.textContent?.replace(/\d+$/, ''))
    expect(names.indexOf('Security')).toBe(names.indexOf('Analytics') + 1)
  })

  it('shows Log search under Insights, after Security, with an icon and no count', async () => {
    renderApp('/')
    const link = await screen.findByRole('link', { name: /^Log search/ })
    expect(link).toHaveTextContent(/^Log search$/)
    expect(link).toHaveAttribute('href', '/logs')
    expect(link.querySelector('svg')).not.toBeNull()
    const names = within(screen.getByRole('navigation', { name: 'Main' }))
      .getAllByRole('link')
      .map((item) => item.textContent?.replace(/\d+$/, ''))
    expect(names.indexOf('Log search')).toBe(names.indexOf('Security') + 1)
  })

  it('shows Analytics between Storage and Settings', async () => {
    renderApp('/')
    await screen.findByRole('link', { name: /^Analytics/ })
    const names = within(screen.getByRole('navigation', { name: 'Main' }))
      .getAllByRole('link')
      .map((link) => link.textContent?.replace(/\d+$/, ''))
    expect(names.indexOf('Analytics')).toBe(names.indexOf('Storage') + 1)
    expect(names.indexOf('Settings')).toBe(names.indexOf('Analytics') + 3)
  })

  it('is active on every analytics tab', async () => {
    renderApp('/analytics/reliability')
    const link = await screen.findByRole('link', { name: /^Analytics/ })
    expect(link.className).toMatch(/active/)
    expect(screen.getByRole('link', { name: /^Storage/ }).className).not.toMatch(/active/)
  })
})

describe('Sidebar groups', () => {
  it('groups and icons; Services active on a service page; account menu lists the three rules', async () => {
    ssoOn()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/overview`)
    const main = await nav()
    for (const group of ['Infrastructure', 'Insights', 'Admin']) expect(within(main).getByText(group)).toBeInTheDocument()
    const links = within(main).getAllByRole('link')
    expect(links.map((link) => link.textContent?.replace(/\d+$/, ''))).toEqual([
      'Overview',
      'Services',
      'Clusters',
      'Tasks',
      'Networks',
      'Storage',
      'Analytics',
      'Security',
      'Log search',
      'Settings',
    ])
    // Every nav item carries an icon.
    for (const link of links) expect(link.querySelector('svg')).not.toBeNull()
    expect(within(main).getByRole('link', { name: /^Services/ }).className).toMatch(/active/)
    expect(within(main).getByRole('link', { name: /^Clusters/ }).className).not.toMatch(/active/)

    // The account rules: My access always, Change password with a password, Link SSO account only when unlinked and SSO is on.
    const menu = within(await openAccountMenu(userEvent.setup()))
    expect(menu.getByRole('link', { name: 'My access' })).toBeInTheDocument()
    expect(menu.getByRole('button', { name: 'Change password' })).toBeInTheDocument()
    expect(await menu.findByRole('button', { name: LINK })).toBeInTheDocument()
    expect(menu.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
  })

  it('names the account button after the user and reports its state', async () => {
    renderApp('/')
    const button = await screen.findByRole('button', { name: 'Account menu for sam' })
    expect(button).toHaveAttribute('aria-expanded', 'false')
    await userEvent.setup().click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
  })

  it('g s from the keyboard lands on /services and ? opens the shortcuts dialog', async () => {
    renderApp('/')
    await nav()
    const user = userEvent.setup()
    await user.keyboard('gs')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/services'))
    await user.keyboard('?')
    expect(await screen.findByRole('dialog', { name: 'Keyboard shortcuts' })).toBeInTheDocument()
  })
})
