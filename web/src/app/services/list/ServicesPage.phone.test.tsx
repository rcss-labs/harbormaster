import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  localStorage.clear()
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  // Let requests still in flight from an unmounted app finish, so they cannot reach the next test's handlers.
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const rowOf = async (name: string) => {
  const table = await screen.findByRole('table', { name: 'Services' })
  return await waitFor(() => {
    const row = within(table)
      .getAllByRole('row')
      .find((candidate) => within(candidate).queryByRole('link', { name }))
    if (!row) throw new Error(`no row for ${name}`)
    return row
  })
}

describe('Services page on phones', () => {
  it('views and status render as chips', async () => {
    renderApp('/services')
    const views = await screen.findByRole('group', { name: 'Saved views' })
    expect(await within(views).findByRole('button', { name: 'All 15' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(views).getByRole('button', { name: 'Problems 3' })).toHaveAttribute('aria-pressed', 'false')
    const status = screen.getByRole('group', { name: 'Status' })
    expect(within(status).getAllByRole('button').map((button) => button.textContent)).toEqual(
      expect.arrayContaining(['All 15', 'Failing 1', 'Warning 1', 'In progress 1', 'Steady 12']),
    )
    expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument()
    expect(screen.getByRole('searchbox', { name: 'Filter services' })).toHaveAttribute('data-hm-filter')
    expect(screen.getByRole('button', { name: 'Sort and fields' })).toBeInTheDocument()
  })

  it('row shows line-2 values for the visible fields only', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    const row = await rowOf('payments-api')
    const line2 = row.querySelector('.hm-l2') as HTMLElement
    await waitFor(() => expect(line2).toHaveTextContent('CPU 41 %'))
    for (const text of ['core-services', 'homelab', '3/3 tasks', ':42', '1.18.2', 'Mem 63 %']) {
      expect(line2).toHaveTextContent(text)
    }
    // Status and tag are on the first line with the name.
    expect(row.querySelector('.hm-l1')).toHaveTextContent('Prod')

    await user.click(screen.getByRole('button', { name: 'Sort and fields' }))
    const fields = within(screen.getByRole('dialog', { name: 'Sort and fields' })).getByRole('group', { name: 'Fields on each row' })
    await user.click(within(fields).getByRole('button', { name: 'Image tag' }))
    await user.click(within(fields).getByRole('button', { name: 'Memory' }))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(line2).not.toHaveTextContent('Mem 63 %'))
    expect(line2).not.toHaveTextContent('1.18.2')
    expect(line2).toHaveTextContent('CPU 41 %')
  })

  it('action sheet lists the seven rows with the disabled reason', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await rowOf('notifications')
    await user.click(screen.getByRole('button', { name: 'Actions for notifications' }))
    const sheet = await screen.findByRole('menu', { name: 'Actions for notifications' })
    const items = within(sheet).getAllByRole('menuitem')
    expect(items.map((item) => item.textContent)).toEqual([
      'Open service',
      'Details',
      'Logs',
      'Tasks',
      'RestartA rollout is already running',
      'Deploy',
      'PinPin to Overview and Services',
    ])
    const restart = within(sheet).getByRole('menuitem', { name: /^Restart/ })
    expect(restart).toBeDisabled()
    expect(within(restart).getByText('A rollout is already running')).toBeInTheDocument()
    expect(within(sheet).getByRole('menuitem', { name: 'Deploy' })).toHaveAttribute('href', expect.stringMatching(/\/deploy$/))
  })

  it('details sheet shows CPU and Memory', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await rowOf('payments-api')
    await user.click(screen.getByRole('button', { name: 'Actions for payments-api' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Details' }))
    const details = await screen.findByRole('dialog', { name: 'payments-api' })
    await waitFor(() => expect(within(details).getByText('CPU', { selector: 'dt' }).nextElementSibling).toHaveTextContent('41 %'))
    expect(within(details).getByText('Memory', { selector: 'dt' }).nextElementSibling).toHaveTextContent('63 %')
    expect(within(within(details).getByRole('group', { name: 'Open' })).getAllByRole('link').map((link) => link.textContent)).toEqual([
      'Overview',
      'Tasks',
      'Logs',
      'Metrics',
      'Deploy',
      'History',
    ])
  })

  it('Insights off reads as unavailable, never zero', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    const row = await rowOf('edge-nginx')
    const line2 = row.querySelector('.hm-l2') as HTMLElement
    await waitFor(() => expect(line2).toHaveTextContent('CPU –'))
    expect(line2).toHaveTextContent('Mem –')
    expect(line2).not.toHaveTextContent(/CPU 0|Mem 0/)
    // The reason is text on the page, not only a tooltip: a footer note explains the dashes.
    expect(screen.getByText('– Container Insights is off for this cluster.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Actions for edge-nginx' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Details' }))
    const details = await screen.findByRole('dialog', { name: 'edge-nginx' })
    // ...and the Details sheet says it beside each dash.
    expect(within(details).getByText('CPU', { selector: 'dt' }).nextElementSibling).toHaveTextContent('– Container Insights is off for this cluster')
    expect(within(details).getByText('Memory', { selector: 'dt' }).nextElementSibling).toHaveTextContent('– Container Insights is off for this cluster')
  })
})
