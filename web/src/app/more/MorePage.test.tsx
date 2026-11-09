import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ALERTS, AUTH_OPTIONS_ON, ME_OMER, ME_SSO_USER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
  localStorage.clear()
  delete document.documentElement.dataset.theme
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const section = async (name: string) => within(await screen.findByRole('region', { name }))

describe('MorePage', () => {
  it('admin rows', async () => {
    renderApp('/more')
    const signedIn = await section('Signed in')
    expect(signedIn.getByText(/Signed in as/)).toHaveTextContent('Signed in as sam · Global admin')
    expect(signedIn.getByText('sam@example.test · Password, Break-glass')).toBeInTheDocument()
    await waitFor(() => expect(signedIn.getByRole('button', { name: /^Activity/ })).toHaveTextContent(`${ALERTS.counts.danger} problems`))

    const infra = await section('Infrastructure')
    expect(infra.getByRole('link', { name: 'Networks' })).toHaveAttribute('href', '/networks')
    expect(infra.getByRole('link', { name: 'Storage' })).toHaveAttribute('href', '/storage')
    const insights = await section('Insights')
    expect(insights.getByRole('link', { name: 'Analytics' })).toHaveAttribute('href', '/analytics/delivery')
    expect(insights.getByRole('link', { name: 'Log search' })).toHaveAttribute('href', '/logs')

    const admin = await section('Admin')
    const hrefs: Record<string, string> = {
      General: '/settings/general',
      'AWS accounts': '/settings/accounts',
      'Users & permissions': '/settings/users',
      Tags: '/settings/tags',
      'Single sign-on': '/settings/sso',
      'Audit log': '/settings/audit',
    }
    for (const [name, href] of Object.entries(hrefs)) expect(admin.getByRole('link', { name: new RegExp(`^${name}`) })).toHaveAttribute('href', href)
    await waitFor(() => expect(admin.getByRole('link', { name: /^Single sign-on/ })).toHaveTextContent('Off'))
  })

  it('shows On for single sign-on when it is enabled', async () => {
    server.use(http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)))
    renderApp('/more')
    const admin = await section('Admin')
    await waitFor(() => expect(admin.getByRole('link', { name: /^Single sign-on/ })).toHaveTextContent('On'))
  })

  it('non-admin rows show My access instead of Admin', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/more')
    const account = await section('Account')
    expect(account.getByRole('link', { name: 'My access' })).toHaveAttribute('href', '/me/access')
    expect(screen.queryByRole('region', { name: 'Admin' })).not.toBeInTheDocument()
    expect((await section('Signed in')).getByText(/Signed in as/)).toHaveTextContent('Signed in as omer · User')
  })

  it('Change password only with has_password', async () => {
    renderApp('/more')
    expect((await section('Account')).getByRole('link', { name: 'Change password' })).toHaveAttribute('href', '/password')
  })

  it('has no Change password row for an SSO-only account', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_SSO_USER)))
    renderApp('/more')
    const account = await section('Account')
    expect(account.getByRole('button', { name: /^Theme/ })).toBeInTheDocument()
    expect(account.queryByRole('link', { name: 'Change password' })).not.toBeInTheDocument()
    expect(account.queryByRole('button', { name: /^Link SSO account/ })).not.toBeInTheDocument()
  })

  it('Link SSO account follows the sidebar rule', async () => {
    server.use(http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)))
    renderApp('/more')
    const link = await (await section('Account')).findByRole('button', { name: /^Link SSO account/ })
    await userEvent.setup().click(link)
    expect(await screen.findByRole('dialog', { name: 'Link your SSO account' })).toBeInTheDocument()
  })

  it('omits Link SSO account while single sign-on is not configured', async () => {
    renderApp('/more')
    const account = await section('Account')
    expect(account.getByRole('button', { name: /^Theme/ })).toBeInTheDocument()
    expect(account.queryByRole('button', { name: /^Link SSO account/ })).not.toBeInTheDocument()
  })

  it('Theme row toggles', async () => {
    document.documentElement.dataset.theme = 'light'
    renderApp('/more')
    const theme = await (await section('Account')).findByRole('button', { name: /^Theme/ })
    expect(theme).toHaveTextContent('Light')
    await userEvent.setup().click(theme)
    expect(document.documentElement.dataset.theme).toBe('dark')
    await waitFor(() => expect(theme).toHaveTextContent('Dark'))
  })

  it('Sign out failure toast text', async () => {
    server.use(http.post('/api/auth/logout', () => HttpResponse.json({ error: { code: 'internal', message: 'boom' } }, { status: 500 })))
    renderApp('/more')
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Sign out' }))
    expect(await screen.findByText('Sign out failed on the server. You were signed out in this browser.')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/login'))
  })

  it('shows the empty pinned text', async () => {
    renderApp('/more')
    expect(await (await section('Pinned')).findByText('Pin a service or cluster from its page.')).toBeInTheDocument()
  })

  it('lists a pinned service as a row', async () => {
    localStorage.setItem('harbormaster-pinned:sam', JSON.stringify([{ kind: 'service', id: 'svc-1', name: 'payments-api', cluster: 'core-services' }]))
    renderApp('/more')
    const pinned = await section('Pinned')
    const row = await pinned.findByRole('link', { name: /payments-api/ })
    expect(row).toHaveAttribute('href', '/services/svc-1/overview')
    expect(row).toHaveTextContent('core-services')
    expect(pinned.queryByText('Pin a service or cluster from its page.')).not.toBeInTheDocument()
  })

  it('shows the footer', async () => {
    renderApp('/more')
    expect(await screen.findByText('Harbormaster · Running on ECS · harbormaster/harbormaster-server')).toBeInTheDocument()
  })

  it('opens the activity sheet from the Activity row', async () => {
    renderApp('/more')
    await userEvent.setup().click(await screen.findByRole('button', { name: /^Activity/ }))
    expect(await screen.findByRole('dialog', { name: 'Activity' })).toBeInTheDocument()
  })
})

describe('MorePage on desktop', () => {
  it('redirects /more to /', async () => {
    restoreMedia()
    restoreMedia = installPhoneMedia(false)
    renderApp('/more')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/$/))
  })
})
