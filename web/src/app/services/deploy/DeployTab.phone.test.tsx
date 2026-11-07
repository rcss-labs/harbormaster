import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { CHANGESET_APPLIED, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { imageTagsHandler } from '../../../test/imageTags'
import { server } from '../../../test/msw'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

const route = `/services/${SERVICE_PAYMENTS.id}/deploy`

let restorePhone: () => void
beforeEach(() => {
  restorePhone = installPhoneMedia(true)
})
afterEach(() => restorePhone())

const desiredCount = () => within(screen.getByRole('region', { name: 'Service' })).getByRole('spinbutton', { name: 'Desired count' })
const bar = () => screen.getByRole('region', { name: 'Pending changes' })

async function openDeployTab() {
  const view = renderApp(route)
  await waitFor(() => desiredCount())
  await screen.findByRole('button', { name: 'Add variable' })
  return view
}

async function setDesiredCount(user: ReturnType<typeof userEvent.setup>, value: string) {
  const field = desiredCount()
  await user.clear(field)
  if (value) await user.type(field, value)
}

describe('DeployTab on a phone', () => {
  it('has no bar until something changes, then the bar shows the count and opens the pending sheet', async () => {
    const user = userEvent.setup()
    await openDeployTab()
    expect(screen.queryByRole('region', { name: 'Pending changes' })).not.toBeInTheDocument()

    await setDesiredCount(user, '5')
    expect(within(bar()).getByText('1 change')).toBeInTheDocument()
    await user.click(within(bar()).getByRole('button', { name: 'Pending changes: 1 change' }))

    const sheet = screen.getByRole('dialog', { name: 'Pending changes' })
    expect(within(sheet).getByText(/Nothing reaches AWS until you confirm in the review step\./)).toBeInTheDocument()
    expect(within(sheet).getByLabelText('Desired count')).toHaveTextContent('3')
    expect(within(sheet).getByLabelText('Desired count')).toHaveTextContent('5')
  })

  it('Review opens the review sheet with the same strings and Apply toasts', async () => {
    const user = userEvent.setup()
    server.use(http.post('/api/services/:id/changeset', () => HttpResponse.json(CHANGESET_APPLIED)))
    await openDeployTab()
    await setDesiredCount(user, '5')
    await user.click(within(bar()).getByRole('button', { name: 'Review…' }))

    const sheet = screen.getByRole('dialog', { name: 'Review changes to payments-api' })
    expect(within(sheet).getByRole('button', { name: 'Keep editing' })).toBeInTheDocument()
    expect(within(sheet).getByLabelText('Reason')).toBeInTheDocument()
    await user.click(within(sheet).getByRole('button', { name: /^(Apply|Register and deploy|Deploy)/ }))

    expect(await screen.findByText(/^(Deploying|Applied)/)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Pending changes' })).not.toBeInTheDocument()
  })

  it('Discard puts every draft back, as on desktop, and the bar goes away', async () => {
    const user = userEvent.setup()
    await openDeployTab()
    await setDesiredCount(user, '5')
    await user.click(within(bar()).getByRole('button', { name: 'Discard' }))
    expect(desiredCount()).toHaveValue(3)
    expect(screen.queryByRole('region', { name: 'Pending changes' })).not.toBeInTheDocument()
  })

  it('problems keep the bar visible and block Review', async () => {
    const user = userEvent.setup()
    await openDeployTab()
    await setDesiredCount(user, '5')
    await setDesiredCount(user, '')
    expect(within(bar()).getByRole('button', { name: 'Review…' })).toBeDisabled()
    await user.click(within(bar()).getByRole('button', { name: /^Pending changes:/ }))
    expect(within(screen.getByRole('dialog', { name: 'Pending changes' })).getByText(/Desired count must be a whole number between 0 and 50/)).toBeInTheDocument()
  })

  it('sets data-hm-bar while the bar shows and clears it after Discard and on unmount', async () => {
    const user = userEvent.setup()
    const { unmount } = await openDeployTab()
    expect(document.documentElement).not.toHaveAttribute('data-hm-bar')
    await setDesiredCount(user, '5')
    expect(document.documentElement).toHaveAttribute('data-hm-bar')
    await user.click(within(bar()).getByRole('button', { name: 'Discard' }))
    expect(document.documentElement).not.toHaveAttribute('data-hm-bar')
    await setDesiredCount(user, '6')
    expect(document.documentElement).toHaveAttribute('data-hm-bar')
    unmount()
    expect(document.documentElement).not.toHaveAttribute('data-hm-bar')
  })

  it('a long tag list opens a tall sheet with its own search; picking a tag edits the draft', async () => {
    const user = userEvent.setup()
    server.use(imageTagsHandler())
    await openDeployTab()
    await user.click(screen.getByRole('combobox', { name: 'Image tag' }))

    const sheet = await screen.findByRole('dialog', { name: 'Image tag' })
    expect(within(sheet).getByRole('searchbox', { name: 'Search image tags' })).toBeInTheDocument()
    await user.click(await within(sheet).findByRole('option', { name: /1\.18\.4/ }))
    expect(screen.queryByRole('dialog', { name: 'Image tag' })).not.toBeInTheDocument()
    expect(within(bar()).getByText('1 change')).toBeInTheDocument()
  })
})
