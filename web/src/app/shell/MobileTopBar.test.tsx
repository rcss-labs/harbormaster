import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ACCOUNT_STAGING, POLLER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const topBar = async () => within(await screen.findByRole('banner'))

describe('MobileTopBar', () => {
  it('title and sub come from the crumbs', async () => {
    renderApp('/analytics/delivery')
    const bar = await topBar()
    expect(await bar.findByText('Delivery')).toBeInTheDocument()
    expect(bar.getByText('Analytics')).toBeInTheDocument()
  })

  it('links back to the previous crumb with its aria-label', async () => {
    renderApp('/analytics/delivery')
    const bar = await topBar()
    const back = await bar.findByRole('link', { name: 'Back to Analytics' })
    expect(back).toHaveAttribute('href', '/analytics/delivery')
  })

  it('shows the logo linking to Overview when there is one crumb', async () => {
    renderApp('/clusters')
    const bar = await topBar()
    await bar.findByText('Clusters')
    expect(bar.getByRole('link', { name: 'Harbormaster, go to Overview' })).toHaveAttribute('href', '/')
    expect(bar.queryByRole('link', { name: /^Back to/ })).not.toBeInTheDocument()
  })

  it('scope chip summarises the scope and opens the sheet', async () => {
    renderApp('/clusters')
    const bar = await topBar()
    const chip = await bar.findByRole('button', { name: /^Scope:/ })
    expect(chip).toHaveTextContent('All')
    const user = userEvent.setup()
    await user.click(chip)
    const sheet = await screen.findByRole('dialog', { name: 'Scope' })
    await user.selectOptions(within(sheet).getByLabelText('Account filter'), ACCOUNT_STAGING.account_id)
    await waitFor(() => expect(screen.getByRole('button', { name: /^Scope:/ })).toHaveTextContent(ACCOUNT_STAGING.name))
    await user.click(within(sheet).getByRole('button', { name: 'Clear scope' }))
    await waitFor(() => expect(screen.getByRole('button', { name: /^Scope:/ })).toHaveTextContent(/^All$/))
    await user.click(within(sheet).getByRole('button', { name: 'Done' }))
    expect(screen.queryByRole('dialog', { name: 'Scope' })).not.toBeInTheDocument()
  })

  it('freshness chip reads N/M with the full sentence as aria-label and opens the sheet', async () => {
    renderApp('/clusters')
    const total = POLLER.items.length
    const bar = await topBar()
    const chip = await bar.findByRole('button', { name: `${total} of ${total} regions current` })
    expect(chip).toHaveTextContent(`${total}/${total}`)
    await userEvent.setup().click(chip)
    const sheet = await screen.findByRole('dialog', { name: 'Data freshness' })
    expect(sheet).toHaveTextContent('homelab-admin / eu-west-1')
  })

  it('offers search and drops the desktop-only buttons', async () => {
    renderApp('/clusters')
    const bar = await topBar()
    expect(await bar.findByRole('button', { name: 'Search or run a command' })).toBeInTheDocument()
    expect(bar.queryByRole('button', { name: 'Toggle row density' })).not.toBeInTheDocument()
    expect(bar.queryByRole('button', { name: 'Keyboard shortcuts' })).not.toBeInTheDocument()
  })

  it('omits the freshness chip until the poller answers', async () => {
    server.use(http.get('/api/poller', () => new HttpResponse(null, { status: 500 })))
    renderApp('/clusters')
    const bar = await topBar()
    await bar.findByText('Clusters')
    expect(bar.queryByRole('button', { name: /regions current$/ })).not.toBeInTheDocument()
  })
})
