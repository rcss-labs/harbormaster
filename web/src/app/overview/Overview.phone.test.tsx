import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { ServiceDTO } from '../../api/types'
import { ALERTS, SERVICE_DETAILS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'
import { listMatchingAlerts } from './attentionFixtures'

let restoreMedia: () => void
beforeEach(() => {
  localStorage.clear()
  restoreMedia = installPhoneMedia(true)
  server.use(http.get('/api/services', () => HttpResponse.json(listMatchingAlerts())))
})
afterEach(() => restoreMedia())

const resizer = ALERTS.items[0]
const resizerDetail = SERVICE_DETAILS[resizer.service_id]
const accountAlert = ALERTS.items.find((alert) => alert.service_id === '')!
const useDetail = (detail: ServiceDTO) => server.use(http.get(`/api/services/${detail.id}`, () => HttpResponse.json(detail)))

const attentionRows = async () => {
  const region = await screen.findByRole('region', { name: 'Needs attention' })
  await within(region).findByText(resizer.title)
  return Array.from(region.querySelectorAll<HTMLElement>('[data-testid="alert-row"]'))
}

describe('Overview on a phone', () => {
  it('KPI grid is three across and the Services tile still links to the problems view', async () => {
    renderApp('/')
    const strip = await screen.findByRole('list', {
      name: 'Estate key figures',
    })
    expect(strip.style.gridTemplateColumns).toBe('repeat(3, minmax(0, 1fr))')
    expect(within(strip).getByRole('link', { name: /Services/ })).toHaveAttribute('href', '/services?view=problems')
  })

  it('severity filter is a chip group with the same counts', async () => {
    renderApp('/')
    const rows = await attentionRows()
    const group = screen.getByRole('group', { name: 'Severity' })
    expect(within(group).getByRole('button', { name: `All ${rows.length}` })).toHaveAttribute('aria-pressed', 'true')
    expect(within(group).getByRole('button', { name: /^Failing \d/ })).toBeInTheDocument()
  })

  it('attention rows keep the cause and move Logs, Tasks and Restart into one sheet', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const row = (await attentionRows())[0]
    expect(within(row).getByRole('link', { name: resizer.title })).toHaveAttribute('data-hm-rl')
    expect(within(row).queryByRole('link', { name: 'Logs' })).not.toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Restart' })).not.toBeInTheDocument()
    await user.click(
      within(row).getByRole('button', {
        name: `Actions for ${resizer.service}`,
      }),
    )
    const sheet = await screen.findByRole('dialog', {
      name: `Actions for ${resizer.service}`,
    })
    expect(within(sheet).getByRole('link', { name: 'Logs' })).toHaveAttribute('href', `/services/${resizer.service_id}/logs`)
    expect(within(sheet).getByRole('link', { name: 'Tasks' })).toHaveAttribute('href', `/services/${resizer.service_id}/tasks`)
    await waitFor(() => expect(within(sheet).getByRole('button', { name: 'Restart' })).toBeEnabled())
  })

  it('the sheet states why Restart is disabled without Operate', async () => {
    useDetail({
      ...resizerDetail,
      can: { ...resizerDetail.can, operate: false },
    })
    const user = userEvent.setup()
    renderApp('/')
    const row = (await attentionRows())[0]
    await user.click(
      within(row).getByRole('button', {
        name: `Actions for ${resizer.service}`,
      }),
    )
    const sheet = await screen.findByRole('dialog', {
      name: `Actions for ${resizer.service}`,
    })
    const restart = within(sheet).getByRole('button', { name: /^Restart/ })
    await waitFor(() => expect(restart).toBeDisabled())
    expect(restart).toHaveTextContent(`You need Operate on ${resizerDetail.tag.name}`)
  })

  it('an account row offers Open accounts in its sheet to an admin', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const rows = await attentionRows()
    const accountRow = rows.find((candidate) => candidate.textContent?.includes(accountAlert.title))!
    await user.click(
      within(accountRow).getByRole('button', {
        name: `Actions for ${accountAlert.title}`,
      }),
    )
    const sheet = await screen.findByRole('dialog', {
      name: `Actions for ${accountAlert.title}`,
    })
    expect(within(sheet).getByRole('link', { name: 'Open accounts' })).toHaveAttribute('href', '/settings/accounts')
  })
})

describe('Overview estate on a phone', () => {
  it('keeps group headers and puts each service behind one actions button', async () => {
    localStorage.setItem('harbormaster-overview-layout', 'estate')
    const user = userEvent.setup()
    renderApp('/')
    const region = await screen.findByRole('region', { name: 'Services' })
    const heads = await within(region).findAllByRole('button', {
      expanded: true,
    })
    expect(heads.length).toBeGreaterThan(0)
    expect(within(region).queryByRole('link', { name: 'Logs' })).not.toBeInTheDocument()
    const first = (await within(region).findAllByRole('button', { name: /^Actions for / }))[0]
    await user.click(first)
    const sheet = await screen.findByRole('dialog', {
      name: first.getAttribute('aria-label') ?? '',
    })
    expect(within(sheet).getByRole('link', { name: 'Logs' })).toBeInTheDocument()
    expect(within(sheet).getByRole('link', { name: 'Tasks' })).toBeInTheDocument()
  })
})
