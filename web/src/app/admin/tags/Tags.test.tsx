import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { TagAssignmentsDTO, TagImpactDTO } from '../../../api/types'
import { ASSIGNMENTS, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'
import { server } from '../../../test/msw'

const STAGING_TAG_ID = 2

const open = (name: string) => fireEvent.click(screen.getByRole('button', { name }))

/** Expands the named clusters (each button appears once the assignments have loaded). */
async function expandPath(...names: string[]) {
  for (const name of names) open(await screen.findByRole('button', { name }).then(() => name))
}

/** The fixture's first cluster, repeated so the assignments need several pages. */
function manyClusters(count: number): TagAssignmentsDTO {
  const template = ASSIGNMENTS.clusters[0]
  return {
    ...ASSIGNMENTS,
    clusters: Array.from({ length: count }, (_, index) => ({
      ...template,
      id: `cluster-${index + 1}`,
      name: `cluster-${String(index + 1).padStart(2, '0')}`,
      services: [{ ...template.services[0], id: `svc-${index + 1}`, name: `service-${String(index + 1).padStart(2, '0')}` }],
    })),
  }
}

/** Assignments fixture with payments-api explicitly tagged, so choosing "Inherit" is a real change. */
function assignmentsWithPaymentsTagged(tagId: number): TagAssignmentsDTO {
  return {
    ...ASSIGNMENTS,
    clusters: ASSIGNMENTS.clusters.map((cluster) => ({
      ...cluster,
      services: cluster.services.map((service) =>
        service.name === 'payments-api'
          ? {
              ...service,
              tag_id: tagId,
              effective: {
                id: tagId,
                name: 'Prod',
                color: 'red',
                inherited: false,
              },
            }
          : service,
      ),
    })),
  }
}

describe('Tags', () => {
  it('Delete disabled for Dev', async () => {
    renderApp('/settings/tags')
    expect(await screen.findByRole('heading', { level: 2, name: 'Tags' })).toBeInTheDocument()
    const deleteButtons = await screen.findAllByRole('button', {
      name: 'Delete',
    })
    const devRow = deleteButtons[0].closest('tr') as HTMLElement
    expect(within(devRow).getByText('Dev')).toBeInTheDocument()
    expect(deleteButtons[0]).toBeDisabled()
    expect(deleteButtons[0]).toHaveAttribute('title', 'Dev is the default tag')
    expect(deleteButtons[1]).toBeEnabled()
  })

  it('Rename disabled for Dev, enabled for other tags (spec 6.9: Dev cannot be renamed)', async () => {
    renderApp('/settings/tags')
    const renameButtons = await screen.findAllByRole('button', {
      name: 'Rename',
    })
    const devRow = renameButtons[0].closest('tr') as HTMLElement
    expect(within(devRow).getByText('Dev')).toBeInTheDocument()
    expect(renameButtons[0]).toBeDisabled()
    expect(renameButtons[0]).toHaveAttribute('title', 'Dev is the default tag')
    expect(renameButtons[1]).toBeEnabled()
  })
})

describe('Tags filters', () => {
  it('assignments follow the account filter in the URL', async () => {
    const accounts: (string | null)[] = []
    server.use(
      http.get('/api/tag-assignments', ({ request }) => {
        accounts.push(new URL(request.url).searchParams.get('account'))
        return HttpResponse.json(ASSIGNMENTS)
      }),
    )
    renderApp('/settings/tags?account=731022849906')
    await screen.findByRole('button', { name: 'core-services' })
    expect(accounts).toContain('731022849906')
  })
})

describe('TagDialog', () => {
  it('rejects "9lives" with the validation message', async () => {
    const posts: unknown[] = []
    server.use(
      http.post('/api/tags', async ({ request }) => {
        posts.push(await request.json())
        return HttpResponse.json({}, { status: 201 })
      }),
    )
    renderApp('/settings/tags')
    fireEvent.click(await screen.findByRole('button', { name: 'Create tag' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Name'), {
      target: { value: '9lives' },
    })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create tag' }))
    expect(
      await within(dialog).findByText('Use 1–24 letters, digits, spaces, hyphens or underscores, starting with a letter.'),
    ).toBeInTheDocument()
    expect(posts).toEqual([])
  })
})

describe('DeleteTagDialog', () => {
  it('renders impact counts and names', async () => {
    const impact: TagImpactDTO = {
      clusters: [{ id: 'cluster-a', name: 'staging-web' }],
      services: [
        { id: 'service-a', name: 'web' },
        { id: 'service-b', name: 'api' },
      ],
      users: ['dana', 'omer'],
    }
    server.use(http.get(`/api/tags/${STAGING_TAG_ID}/impact`, () => HttpResponse.json(impact)))
    renderApp('/settings/tags')
    fireEvent.click((await screen.findAllByRole('button', { name: 'Delete' }))[1])
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Delete tag Staging?')).toBeInTheDocument()
    expect(await within(dialog).findByText('1 cluster becomes Dev.')).toBeInTheDocument()
    expect(within(dialog).getByText("2 services go back to inheriting their cluster's tag.")).toBeInTheDocument()
    expect(within(dialog).getByText('2 users lose the permissions they had on Staging: dana, omer.')).toBeInTheDocument()
  })
})

describe('Assignments', () => {
  it('service select includes Inherit option and PUTs null', async () => {
    const bodies: unknown[] = []
    server.use(
      http.get('/api/tag-assignments', () => HttpResponse.json(assignmentsWithPaymentsTagged(3))),
      http.put(`/api/tag-assignments/services/${SERVICE_PAYMENTS.id}`, async ({ request }) => {
        bodies.push(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/tags')
    await expandPath('core-services')
    const select = (await screen.findByLabelText('Tag for payments-api')) as HTMLSelectElement
    const inherit = within(select).getByRole('option', {
      name: 'Inherit (Prod)',
    })
    expect(inherit).toHaveProperty('value', '')
    fireEvent.change(select, { target: { value: '' } })
    await waitFor(() => expect(bodies).toEqual([{ tag_id: null }]))
  })
})

describe('EditTagDialog', () => {
  it('PUTs the chosen tag', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`/api/tag-assignments/services/${SERVICE_PAYMENTS.id}`, async ({ request }) => {
        bodies.push(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/overview`)
    fireEvent.click(await screen.findByRole('button', { name: SERVICE_PAYMENTS.tag.name }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Tag for payments-api')).toBeInTheDocument()
    fireEvent.click(await within(dialog).findByRole('radio', { name: 'Staging' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save tag' }))
    await waitFor(() => expect(bodies).toEqual([{ tag_id: STAGING_TAG_ID }]))
  })
})

describe('Assignments list', () => {
  it('is collapsed by default: one row per cluster with its tag select, no service selects', async () => {
    renderApp('/settings/tags')
    const cluster = await screen.findByRole('button', { name: 'core-services' })
    expect(cluster).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByLabelText('Tag for core-services')).toBeInTheDocument()
    expect(screen.getByLabelText('Tag for staging-web')).toBeInTheDocument()
    expect(screen.queryByLabelText('Tag for payments-api')).not.toBeInTheDocument()
    expect(screen.getByText('Dashed tags are inherited from the cluster')).toBeInTheDocument()
  })

  it('shows each cluster account and region beside its name', async () => {
    renderApp('/settings/tags')
    const row = (await screen.findByRole('button', { name: 'core-services' })).closest('tr') as HTMLElement
    expect(within(row).getByText(/homelab-admin · us-east-1/)).toBeInTheDocument()
  })

  it('expanding a cluster shows its services', async () => {
    renderApp('/settings/tags')
    await expandPath('core-services')
    expect(await screen.findByLabelText('Tag for payments-api')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'core-services' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('pages 15 clusters at a time and keeps open rows when the page changes', async () => {
    server.use(http.get('/api/tag-assignments', () => HttpResponse.json(manyClusters(32))))
    renderApp('/settings/tags')
    await expandPath('cluster-01')
    expect(screen.getAllByLabelText(/^Tag for cluster-/)).toHaveLength(15)
    expect(screen.getByText('1–15 of 32')).toBeInTheDocument()
    expect(screen.getByLabelText('Tag for service-01')).toBeInTheDocument()
    expect(screen.queryByLabelText('Tag for cluster-16')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
    expect(screen.getByText('16–30 of 32')).toBeInTheDocument()
    expect(screen.getByLabelText('Tag for cluster-16')).toBeInTheDocument()
    expect(screen.queryByLabelText('Tag for cluster-01')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
    expect(screen.getAllByLabelText(/^Tag for cluster-/)).toHaveLength(2)

    fireEvent.click(screen.getByRole('button', { name: 'Previous page' }))
    fireEvent.click(screen.getByRole('button', { name: 'Previous page' }))
    expect(screen.getByRole('button', { name: 'cluster-01' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByLabelText('Tag for service-01')).toBeInTheDocument()
  })

  it('shows no pager when the clusters fit one page', async () => {
    renderApp('/settings/tags')
    await screen.findByRole('button', { name: 'core-services' })
    expect(screen.queryByRole('navigation', { name: 'Pagination' })).not.toBeInTheDocument()
  })

  it('changing a cluster tag keeps the row expanded after the refetch', async () => {
    let puts = 0
    server.use(
      http.put(/\/api\/tag-assignments\/clusters\/.+/, () => {
        puts += 1
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/tags')
    await expandPath('core-services')
    const select = await screen.findByLabelText('Tag for core-services')
    fireEvent.change(select, { target: { value: String(STAGING_TAG_ID) } })
    await waitFor(() => expect(puts).toBe(1))
    await screen.findByText(/is now/)
    expect(screen.getByLabelText('Tag for payments-api')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'core-services' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('Expand all opens every cluster on the page and Collapse all closes them again', async () => {
    renderApp('/settings/tags')
    await screen.findByRole('button', { name: 'core-services' })
    fireEvent.click(screen.getByRole('button', { name: 'Expand all' }))
    expect(screen.getByLabelText('Tag for payments-api')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Collapse all' }))
    expect(screen.queryByLabelText('Tag for payments-api')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'core-services' })).toHaveAttribute('aria-expanded', 'false')
  })
})
