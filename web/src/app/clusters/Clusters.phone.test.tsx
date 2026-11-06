import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { CLUSTERS, ME_OMER } from '../../test/fixtures'
import { installPhoneMedia } from '../../test/phone'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

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

const backgroundJobs = () => CLUSTERS.items.find((cluster) => cluster.name === 'background-jobs')!

describe('Clusters on phones', () => {
  it('filters render as chips with counts', async () => {
    renderApp('/clusters')
    const tags = await screen.findByRole('group', { name: 'Tag' })
    await waitFor(() => expect(within(tags).getByRole('button', { name: /^All 6$/ })).toHaveAttribute('aria-pressed', 'true'))
    expect(within(tags).getByRole('button', { name: /^Dev 4$/ })).toHaveAttribute('aria-pressed', 'false')
    const status = screen.getByRole('group', { name: 'Status' })
    expect(within(status).getByRole('button', { name: /^Any status 6$/ })).toBeInTheDocument()
  })

  it('a row keeps the name and status on line one and account, region and tasks on line two', async () => {
    renderApp('/clusters')
    const table = await screen.findByRole('table', { name: 'Clusters' })
    let row: HTMLElement | undefined
    await waitFor(() => {
      row = within(table)
        .getAllByRole('row')
        .find((candidate) => candidate.textContent?.includes('background-jobs'))
      expect(row).toBeDefined()
    })
    if (!row) throw new Error('no background-jobs row')
    const name = within(row).getByRole('link', { name: 'background-jobs' })
    expect(name.closest('[data-hm-cell="name"]')).toBeInTheDocument()
    expect(row.querySelector('[data-hm-cell="status"]')).toBeInTheDocument()
    const where = row.querySelector('[data-hm-cell="where"]')
    expect(where).toHaveTextContent(`${backgroundJobs().account_name} · ${backgroundJobs().region}`)
    expect(row.querySelector('[data-hm-cell="services"]')).toBeNull()
  })

  it('the row action sheet lists the cluster actions, with Edit tag for admins', async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    await user.click(await screen.findByRole('button', { name: 'Actions for background-jobs' }))
    const menu = screen.getByRole('menu', { name: 'Actions for background-jobs' })
    const labels = within(menu)
      .getAllByRole('menuitem')
      .map((item) => item.textContent)
    expect(labels).toEqual(expect.arrayContaining([expect.stringContaining('Open cluster'), expect.stringContaining('Services'), expect.stringContaining('Tasks'), expect.stringContaining('Pin'), expect.stringContaining('Edit tag')]))
  })

  it('non-admins get no Edit tag row in the cluster action sheet', async () => {
    const user = userEvent.setup()
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/clusters')
    await user.click(await screen.findByRole('button', { name: 'Actions for background-jobs' }))
    const menu = screen.getByRole('menu', { name: 'Actions for background-jobs' })
    expect(within(menu).queryByRole('menuitem', { name: /Edit tag/ })).not.toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: /Open cluster/ })).toBeInTheDocument()
  })
})
