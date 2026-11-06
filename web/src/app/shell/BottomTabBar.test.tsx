import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ALERTS, TASKS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
  localStorage.clear()
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const tabs = async () => within(await screen.findByRole('navigation', { name: 'Main' }))

describe('BottomTabBar', () => {
  it('has five tabs and marks the current one', async () => {
    renderApp('/services')
    const bar = await tabs()
    const links = bar.getAllByRole('link')
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['/', '/services', '/clusters', '/tasks', '/more'])
    expect(bar.getByRole('link', { name: /^Services/ })).toHaveAttribute('aria-current', 'page')
    expect(bar.getByRole('link', { name: /^Overview/ })).not.toHaveAttribute('aria-current')
  })

  it.each(['/settings/users', '/networks', '/storage', '/analytics/delivery', '/me/access'])('More is current on %s', async (path) => {
    renderApp(path)
    const bar = await tabs()
    expect(bar.getByRole('link', { name: /^More/ })).toHaveAttribute('aria-current', 'page')
  })

  it('badges and aria-labels come from the data', async () => {
    renderApp('/clusters')
    const bar = await tabs()
    const danger = ALERTS.counts.danger
    const overview = await bar.findByRole('link', { name: `Overview, ${danger} need attention` })
    expect(overview).toHaveTextContent(String(danger))
    const problems = TASKS.counts.stopped_error
    expect(await bar.findByRole('link', { name: `Tasks, ${problems} problems` })).toHaveTextContent(String(problems))
    expect(bar.getByRole('link', { name: 'Clusters' })).toBeInTheDocument()
  })

  it('shows the account issues on More for admins', async () => {
    server.use(
      http.get('/api/poller', () =>
        HttpResponse.json({
          items: [{ account_id: '1', account_name: 'a', region: 'us-east-1', throttled: false, failing: true, last_error: 'x', last_ok_at: null, tiers: [] }],
        }),
      ),
    )
    renderApp('/clusters')
    const bar = await tabs()
    expect(await bar.findByRole('link', { name: 'More, 1 account issues' })).toBeInTheDocument()
  })
})
