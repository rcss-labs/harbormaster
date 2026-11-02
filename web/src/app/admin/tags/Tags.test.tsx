import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { TagAssignmentsDTO, TagImpactDTO } from '../../../api/types'
import { ASSIGNMENTS, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'
import { server } from '../../../test/msw'

const STAGING_TAG_ID = 2

const open = (name: string) => fireEvent.click(screen.getByRole('button', { name }))

/** Expand account, region and cluster in turn (each button appears once its parent is open). */
async function expandPath(...names: string[]) {
  for (const name of names) open(await screen.findByRole('button', { name }).then(() => name))
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
    await screen.findByRole('button', { name: 'homelab-admin' })
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
    await expandPath('homelab-admin', 'us-east-1', 'core-services')
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

describe('Assignments tree', () => {
  it('is collapsed by default: levels shown, no cluster selects', async () => {
    renderApp('/settings/tags')
    const account = await screen.findByRole('button', {
      name: 'homelab-admin',
    })
    expect(account).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByRole('button', { name: 'staging-example' })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('button', { name: 'us-east-1' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/^Tag for /)).not.toBeInTheDocument()
    expect(screen.getByText(/^5 clusters · \d+ services$/)).toBeInTheDocument()
    expect(screen.getByText('Dashed tags are inherited from the cluster')).toBeInTheDocument()
  })

  it('account row shows the distinct tags inside it', async () => {
    renderApp('/settings/tags')
    const row = (await screen.findByRole('button', { name: 'homelab-admin' })).closest('tr') as HTMLElement
    expect(within(row).getByText('Prod')).toBeInTheDocument()
    expect(within(row).getAllByText('Dev')).toHaveLength(1)
    expect(within(row).queryByText('Staging')).not.toBeInTheDocument()
  })

  it('expanding account then region shows its clusters with their selects; other regions stay hidden', async () => {
    renderApp('/settings/tags')
    await expandPath('homelab-admin', 'us-east-1')
    expect(await screen.findByLabelText('Tag for core-services')).toBeInTheDocument()
    expect(screen.getByLabelText('Tag for ci-tools')).toBeInTheDocument()
    expect(screen.queryByLabelText('Tag for eu-apps')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Tag for staging-web')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Tag for payments-api')).not.toBeInTheDocument()
  })

  it('expanding a cluster shows its services', async () => {
    renderApp('/settings/tags')
    await expandPath('homelab-admin', 'us-east-1', 'core-services')
    expect(await screen.findByLabelText('Tag for payments-api')).toBeInTheDocument()
  })

  it('changing a cluster tag keeps the tree expanded after the refetch', async () => {
    let puts = 0
    server.use(
      http.put(/\/api\/tag-assignments\/clusters\/.+/, () => {
        puts += 1
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/tags')
    await expandPath('homelab-admin', 'us-east-1', 'core-services')
    const select = await screen.findByLabelText('Tag for core-services')
    fireEvent.change(select, { target: { value: String(STAGING_TAG_ID) } })
    await waitFor(() => expect(puts).toBe(1))
    await screen.findByText(/is now/)
    expect(screen.getByLabelText('Tag for core-services')).toBeInTheDocument()
    expect(screen.getByLabelText('Tag for payments-api')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'homelab-admin' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('Expand all opens every level and Collapse all closes them again', async () => {
    renderApp('/settings/tags')
    await screen.findByRole('button', { name: 'homelab-admin' })
    fireEvent.click(screen.getByRole('button', { name: 'Expand all' }))
    expect(screen.getByLabelText('Tag for staging-web')).toBeInTheDocument()
    expect(screen.getByLabelText('Tag for eu-apps')).toBeInTheDocument()
    expect(screen.getByLabelText('Tag for payments-api')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Collapse all' }))
    expect(screen.queryByLabelText(/^Tag for /)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'homelab-admin' })).toHaveAttribute('aria-expanded', 'false')
  })
})
