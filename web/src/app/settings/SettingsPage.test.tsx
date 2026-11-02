import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { ME_OMER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const location = () => screen.getByTestId('location')

describe('SettingsPage', () => {
  it('renders the four tabs and the General tab by default', async () => {
    renderApp('/settings')
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeInTheDocument()
    expect(location()).toHaveTextContent('/settings/general')
    for (const label of ['General', 'AWS accounts', 'Users & permissions', 'Audit log']) {
      expect(screen.getByRole('link', { name: label })).toBeInTheDocument()
    }
    expect(await screen.findByRole('heading', { level: 2, name: 'Tags' })).toBeInTheDocument()
  })

  it('tabs switch content', async () => {
    renderApp('/settings/general')
    await userEvent.click(await screen.findByRole('link', { name: 'Users & permissions' }))
    expect(await screen.findByLabelText('Preset for omer on Dev')).toBeInTheDocument()
    expect(location()).toHaveTextContent('/settings/users')
  })

  it.each([
    ['/admin/tags', '/settings/general'],
    ['/admin/users', '/settings/users'],
    ['/admin/accounts', '/settings/accounts'],
    ['/admin/audit', '/settings/audit'],
  ])('old admin URL %s redirects to %s', async (from, to) => {
    renderApp(from)
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeInTheDocument()
    expect(location()).toHaveTextContent(to)
  })

  it('unknown tab falls back to general', async () => {
    renderApp('/settings/nope')
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeInTheDocument()
    expect(location()).toHaveTextContent('/settings/general')
  })

  it('non-admins get the admin-only panel', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/settings/users')
    expect(await screen.findByText('No access')).toBeInTheDocument()
    expect(screen.getByText('Only global admins manage users and permissions.')).toBeInTheDocument()
    // The panel replaces the page: no Settings heading and no tab bar above it.
    expect(screen.queryByRole('heading', { level: 1, name: 'Settings' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Audit log' })).not.toBeInTheDocument()
  })

  it('non-admins see the settings copy on General', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/settings/general')
    expect(await screen.findByText('Only global admins change settings.')).toBeInTheDocument()
  })

  it('crumbs read Settings / tab label', async () => {
    renderApp('/settings/audit')
    const crumbs = await screen.findByRole('navigation', { name: 'Breadcrumb' })
    await within(crumbs).findByText('Audit log')
    expect(crumbs).toHaveTextContent('Settings/Audit log')
  })
})
