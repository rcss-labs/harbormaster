import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ALERTS, SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const originalWidth = window.innerWidth
const setWidth = (width: number) => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: width })
}

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
  setWidth(1440)
})

afterEach(async () => {
  setWidth(originalWidth)
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const sidebarToggle = () => screen.findByRole('button', { name: /sidebar$/i })
const railToggle = () => screen.findByRole('button', { name: /^Activity/ })

describe('collapsible sidebar', () => {
  it('starts expanded on a wide screen and toggles with the [ key', async () => {
    renderApp('/')
    const toggle = await sidebarToggle()
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const user = userEvent.setup()
    await user.keyboard('[[')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.keyboard('[[')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
  })

  it('toggles from the footer button and remembers the choice', async () => {
    const first = renderApp('/')
    const user = userEvent.setup()
    await user.click(await sidebarToggle())
    expect(await sidebarToggle()).toHaveAttribute('aria-expanded', 'false')
    first.unmount()

    renderApp('/')
    expect(await sidebarToggle()).toHaveAttribute('aria-expanded', 'false')
  })

  it('ignores [ while typing in a field and when a modifier is held', async () => {
    renderApp('/clusters')
    const toggle = await sidebarToggle()
    const user = userEvent.setup()
    await user.click(await screen.findByLabelText('Account filter'))
    await user.keyboard('[[')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    ;(document.activeElement as HTMLElement).blur()
    await user.keyboard('{Control>}[[{/Control}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
  })

  it('ignores [ while a modal dialog is open', async () => {
    renderApp('/')
    const toggle = await sidebarToggle()
    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    dialog.setAttribute('aria-modal', 'true')
    document.body.appendChild(dialog)
    try {
      await userEvent.setup().keyboard('[[')
      expect(toggle).toHaveAttribute('aria-expanded', 'true')
    } finally {
      dialog.remove()
    }
  })

  it('keeps Sign out, Change password reachable from the avatar menu when collapsed', async () => {
    setWidth(1000)
    renderApp('/')
    await sidebarToggle()
    const user = userEvent.setup()
    expect(screen.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument()
    const avatar = await screen.findByRole('button', { name: /^Account menu/ })
    expect(avatar).toHaveAttribute('aria-expanded', 'false')
    await user.click(avatar)
    expect(avatar).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('button', { name: 'Change password' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument()
  })

  it('collapses by itself under 1100px, and an explicit choice wins over that', async () => {
    setWidth(1000)
    renderApp('/')
    const toggle = await sidebarToggle()
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await userEvent.setup().click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
  })

  it('keeps accessible names and tooltips on collapsed items', async () => {
    setWidth(1000)
    renderApp('/')
    const main = await screen.findByRole('navigation', { name: 'Main' })
    const clusters = within(main).getByRole('link', { name: /Clusters/ })
    expect(clusters).toHaveAttribute('title', expect.stringContaining('Clusters'))
    expect(clusters).toHaveAttribute('href', '/clusters')
  })

  it('shows the failing count on Overview and the problem count on Settings only for admins', async () => {
    renderApp('/')
    const main = await screen.findByRole('navigation', { name: 'Main' })
    const overview = within(main).getByRole('link', { name: /Overview/ })
    expect(await within(overview).findByText(String(ALERTS.counts.danger))).toBeInTheDocument()
    expect(within(main).getByRole('link', { name: /Settings/ })).toBeInTheDocument()
  })

  it('shows an amber account-issue count on Settings from the poller status', async () => {
    server.use(
      http.get('/api/poller', () =>
        HttpResponse.json({
          items: [
            { account_id: '1', account_name: 'a', region: 'us-east-1', throttled: false, failing: true, last_error: 'x', last_ok_at: null },
            { account_id: '1', account_name: 'a', region: 'eu-west-1', throttled: true, failing: false, last_error: '', last_ok_at: null },
          ],
        }),
      ),
    )
    renderApp('/')
    const settings = await within(await screen.findByRole('navigation', { name: 'Main' })).findByRole('link', { name: /Settings/ })
    expect(await within(settings).findByLabelText('2 account issues')).toBeInTheDocument()
  })
})

describe('pinned services', () => {
  it('pins from the service header, lists it under Pinned and unpins', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/overview`)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Pin payments-api' }))
    const pinned = await screen.findByRole('navigation', { name: 'Pinned services' })
    expect(within(pinned).getByRole('link', { name: /payments-api/ })).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/overview`)
    expect(JSON.parse(localStorage.getItem('harbormaster-pinned:pavel')!)).toHaveLength(1)

    await user.click(screen.getByRole('button', { name: 'Unpin payments-api' }))
    expect(within(screen.getByRole('navigation', { name: 'Pinned services' })).queryByRole('link')).not.toBeInTheDocument()
  })

  it('shows services pinned earlier', async () => {
    localStorage.setItem('harbormaster-pinned:pavel', JSON.stringify([{ id: 'abc', name: 'edge-nginx', cluster: 'edge-proxy' }]))
    renderApp('/')
    const pinned = await screen.findByRole('navigation', { name: 'Pinned services' })
    expect(within(pinned).getByRole('link', { name: /edge-nginx/ })).toHaveAttribute('href', '/services/abc/overview')
  })
})

describe('top bar', () => {
  it('shows the polling pulse and an Activity toggle with the problem count', async () => {
    renderApp('/')
    expect(await screen.findByText(/live · polled \d+s ago/)).toBeInTheDocument()
    const toggle = await railToggle()
    await waitFor(() => expect(toggle).toHaveTextContent(String(ALERTS.counts.danger)))
  })

  it('hides the pulse when the main column is narrow', async () => {
    setWidth(920)
    renderApp('/')
    await railToggle()
    expect(screen.queryByText(/live · polled/)).not.toBeInTheDocument()
  })

  it('has a search button that names the shortcut for the platform', async () => {
    renderApp('/')
    const button = await screen.findByRole('button', { name: /Search or run a command/ })
    expect(button).toHaveTextContent('Ctrl K')
  })

  it('keeps the account and region filters inside the Scope chip', async () => {
    renderApp('/')
    const scope = await screen.findByRole('group', { name: 'Scope' })
    expect(within(scope).getByLabelText('Account filter')).toBeInTheDocument()
    expect(within(scope).getByLabelText('Region filter')).toBeInTheDocument()
  })
})

describe('activity rail', () => {
  it('is docked beside the page on a wide screen and closes from its toggle', async () => {
    renderApp('/')
    const rail = await screen.findByRole('complementary', { name: 'Activity' })
    expect(screen.queryByRole('dialog', { name: 'Activity' })).not.toBeInTheDocument()
    const toggle = await railToggle()
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await userEvent.setup().click(toggle)
    expect(rail).not.toBeInTheDocument()
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
  })

  it('is closed on a narrow screen, opens as an overlay dialog and closes on Escape', async () => {
    setWidth(1100)
    renderApp('/')
    const toggle = await railToggle()
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('dialog', { name: 'Activity' })).not.toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(toggle)
    expect(await screen.findByRole('dialog', { name: 'Activity' })).toBeInTheDocument()
    expect(screen.queryByRole('complementary', { name: 'Activity' })).not.toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Activity' })).not.toBeInTheDocument()
  })

  it('filters Problems, Changes and All', async () => {
    const base = { ts: new Date().toISOString(), service: { id: 's1', name: 'payments-api', status: 'steady' as const }, cluster: { id: 'c1', name: 'core-services' } }
    server.use(
      http.get('/api/overview', async () => {
        const { OVERVIEW } = await import('../../test/fixtures')
        return HttpResponse.json({
          ...OVERVIEW,
          events: [
            { ...base, source: 'harbormaster', actor: 'pavel', text: 'Restarted service.' },
            { ...base, source: 'ecs', actor: 'ECS', text: 'has reached a steady state.' },
          ],
        })
      }),
    )
    renderApp('/')
    const rail = await screen.findByRole('complementary', { name: 'Activity' })
    const user = userEvent.setup()
    // Default is Problems: danger and warning alerts only.
    expect(await within(rail).findByText('image-resizer')).toBeInTheDocument()
    expect(within(rail).getByText('catalog-api')).toBeInTheDocument()
    expect(within(rail).queryByText('edge-nginx')).not.toBeInTheDocument()
    await user.click(within(rail).getByRole('radio', { name: /^Changes/ }))
    expect(within(rail).queryByText('image-resizer')).not.toBeInTheDocument()
    expect(await within(rail).findByText('pavel')).toBeInTheDocument()
    expect(within(rail).queryByText('has reached a steady state.')).not.toBeInTheDocument()
    await user.click(within(rail).getByRole('radio', { name: /^All/ }))
    expect(within(rail).getByText('edge-nginx')).toBeInTheDocument()
    expect(within(rail).getByText('has reached a steady state.')).toBeInTheDocument()
  })

  it('collapses repeated events into one row with a count', async () => {
    const same = {
      ts: new Date().toISOString(),
      source: 'ecs' as const,
      actor: 'ECS',
      service: { id: 's1', name: 'payments-api', status: 'steady' as const },
      cluster: { id: 'c1', name: 'core-services' },
      text: 'has reached a steady state.',
    }
    server.use(
      http.get('/api/overview', async () => {
        const { OVERVIEW } = await import('../../test/fixtures')
        return HttpResponse.json({ ...OVERVIEW, events: [same, same, same] })
      }),
    )
    renderApp('/')
    const rail = await screen.findByRole('complementary', { name: 'Activity' })
    await userEvent.setup().click(within(rail).getByRole('radio', { name: /^All/ }))
    expect(await within(rail).findByText('×3')).toBeInTheDocument()
    expect(within(rail).getAllByText('has reached a steady state.')).toHaveLength(1)
  })
})
