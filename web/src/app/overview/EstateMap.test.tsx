import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { CLUSTERS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => localStorage.clear())

const map = () => screen.findByRole('region', { name: 'Estate map' })
const clusterId = (name: string) => CLUSTERS.items.find((c) => c.name === name)!.id

describe('EstateMap', () => {
  it('draws one row per account with one cell per cluster', async () => {
    renderApp('/')
    const region = await map()
    const rows = await within(region).findAllByRole('group')
    expect(rows.map((row) => row.getAttribute('aria-label'))).toEqual(['homelab-admin', 'staging-example'])
    expect(within(rows[0]).getAllByRole('link')).toHaveLength(5)
    expect(within(rows[1]).getAllByRole('link')).toHaveLength(1)
  })

  it('labels each cell with cluster, status, tasks and region', async () => {
    renderApp('/')
    const cell = await within(await map()).findByRole('link', { name: /^background-jobs/ })
    expect(cell).toHaveAccessibleName('background-jobs, Needs attention, 2/4 tasks, us-east-1')
    expect(cell).toHaveAttribute('href', `/clusters/${clusterId('background-jobs')}`)
  })

  it('colours healthy cells with the cell-ok token and failing ones with bad', async () => {
    renderApp('/')
    const region = await map()
    const healthy = await within(region).findByRole('link', { name: /^ci-tools/ })
    const failing = within(region).getByRole('link', { name: /^background-jobs/ })
    expect(healthy.getAttribute('style')).toContain('var(--cell-ok)')
    expect(failing.getAttribute('style')).toContain('var(--bad)')
  })

  it('opens the cluster when a cell is clicked', async () => {
    const user = userEvent.setup()
    renderApp('/')
    await user.click(await within(await map()).findByRole('link', { name: /^eu-apps/ }))
    expect(await screen.findByTestId('location')).toHaveTextContent(`/clusters/${clusterId('eu-apps')}`)
  })

  it('stays one row per account with 100 clusters', async () => {
    const base = CLUSTERS.items[0]
    const items = Array.from({ length: 100 }, (_, i) => ({ ...base, id: `c-${i}`, name: `cluster-${i}`, services: [] }))
    server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items })))
    renderApp('/')
    const region = await map()
    expect(await within(region).findAllByRole('link')).toHaveLength(100)
    expect(within(region).getAllByRole('group')).toHaveLength(1)
  })
})
