import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ACCOUNT_HOMELAB, ACCOUNT_STAGING, ME_OMER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const originalWidth = window.innerWidth

beforeEach(() => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: 1440 })
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

afterEach(async () => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: originalWidth })
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const scopeButton = () => screen.findByRole('button', { name: /^Scope:/ })

describe('ScopeControl', () => {
  it('label summarises account, region count and tags', async () => {
    renderApp('/clusters')
    expect(await scopeButton()).toHaveTextContent('All accounts · 3 regions · All tags')
  })

  it('label follows the chosen account, region and tags', async () => {
    renderApp(`/clusters?account=${ACCOUNT_STAGING.account_id}&region=eu-west-1&tag=3`)
    expect(await scopeButton()).toHaveTextContent(`${ACCOUNT_STAGING.name} · eu-west-1 · Prod`)
  })

  it('opens a popover with the account, region and tag controls', async () => {
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.click(await scopeButton())
    const popover = await screen.findByRole('dialog', { name: 'Scope' })
    expect(within(popover).getByLabelText('Account filter')).toBeInTheDocument()
    expect(within(popover).getByLabelText('Region filter')).toBeInTheDocument()
    expect(within(popover).getByRole('button', { name: 'Tag filter' })).toBeInTheDocument()
    await user.selectOptions(within(popover).getByLabelText('Account filter'), ACCOUNT_HOMELAB.account_id)
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(`account=${ACCOUNT_HOMELAB.account_id}`))
    expect(await scopeButton()).toHaveTextContent(`${ACCOUNT_HOMELAB.name} · 3 regions · All tags`)
  })

  it('saves the current scope under a name and applies it later', async () => {
    renderApp(`/clusters?account=${ACCOUNT_STAGING.account_id}&tag=3`)
    const user = userEvent.setup()
    await user.click(await scopeButton())
    const popover = await screen.findByRole('dialog', { name: 'Scope' })
    await user.click(within(popover).getByRole('button', { name: 'Save current' }))
    await user.type(within(popover).getByLabelText('Scope name'), 'Prod staging')
    await user.click(within(popover).getByRole('button', { name: 'Save' }))
    expect(JSON.parse(localStorage.getItem('harbormaster-scopes:sam')!)).toEqual([
      { name: 'Prod staging', account: ACCOUNT_STAGING.account_id, tag: [3] },
    ])

    // Clear everything, then apply the chip.
    await user.selectOptions(within(popover).getByLabelText('Account filter'), '')
    await waitFor(() => expect(screen.getByTestId('search')).not.toHaveTextContent('account='))
    await user.click(within(popover).getByRole('button', { name: 'Prod staging' }))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(`account=${ACCOUNT_STAGING.account_id}`))
    expect(screen.getByTestId('search')).toHaveTextContent('tag=3')
  })

  it('does not save without a name', async () => {
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.click(await scopeButton())
    const popover = await screen.findByRole('dialog', { name: 'Scope' })
    await user.click(within(popover).getByRole('button', { name: 'Save current' }))
    expect(within(popover).getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('removes a saved scope', async () => {
    localStorage.setItem('harbormaster-scopes:sam', JSON.stringify([{ name: 'Prod', tag: [3] }]))
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.click(await scopeButton())
    const popover = await screen.findByRole('dialog', { name: 'Scope' })
    await user.click(within(popover).getByRole('button', { name: 'Remove scope Prod' }))
    expect(within(popover).queryByRole('button', { name: 'Prod' })).not.toBeInTheDocument()
    expect(JSON.parse(localStorage.getItem('harbormaster-scopes:sam')!)).toEqual([])
  })

  it('applying a saved scope does not show tags the user cannot view', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    localStorage.setItem('harbormaster-scopes:omer', JSON.stringify([{ name: 'Prod and dev', tag: [3, 1] }]))
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.click(await scopeButton())
    const popover = await screen.findByRole('dialog', { name: 'Scope' })
    await user.click(within(popover).getByRole('button', { name: 'Prod and dev' }))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('tag=1'))
    expect(screen.getByTestId('search')).not.toHaveTextContent('tag=3')
  })
})
