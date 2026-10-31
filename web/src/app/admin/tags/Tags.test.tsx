import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { TagAssignmentsDTO, TagImpactDTO } from '../../../api/types'
import { ASSIGNMENTS, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'
import { server } from '../../../test/msw'

const STAGING_TAG_ID = 2

/** Assignments fixture with payments-api explicitly tagged, so choosing "Inherit" is a real change. */
function assignmentsWithPaymentsTagged(tagId: number): TagAssignmentsDTO {
  return {
    ...ASSIGNMENTS,
    clusters: ASSIGNMENTS.clusters.map((cluster) => ({
      ...cluster,
      services: cluster.services.map((service) =>
        service.name === 'payments-api' ? { ...service, tag_id: tagId, effective: { id: tagId, name: 'Prod', color: 'red', inherited: false } } : service,
      ),
    })),
  }
}

describe('Tags', () => {
  it('Delete disabled for Dev', async () => {
    renderApp('/admin/tags')
    const deleteButtons = await screen.findAllByRole('button', { name: 'Delete' })
    const devRow = deleteButtons[0].closest('tr') as HTMLElement
    expect(within(devRow).getByText('Dev')).toBeInTheDocument()
    expect(deleteButtons[0]).toBeDisabled()
    expect(deleteButtons[0]).toHaveAttribute('title', 'Dev is the default tag')
    expect(deleteButtons[1]).toBeEnabled()
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
    renderApp('/admin/tags?account=731022849906')
    await screen.findByLabelText('Tag for web')
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
    renderApp('/admin/tags')
    fireEvent.click(await screen.findByRole('button', { name: 'Create tag' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: '9lives' } })
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
    renderApp('/admin/tags')
    fireEvent.click((await screen.findAllByRole('button', { name: 'Delete' }))[1])
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Delete tag Staging?')).toBeInTheDocument()
    expect(await within(dialog).findByText('1 cluster becomes Dev.')).toBeInTheDocument()
    expect(within(dialog).getByText('2 services go back to inheriting their cluster\'s tag.')).toBeInTheDocument()
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
    renderApp('/admin/tags')
    const select = (await screen.findByLabelText('Tag for payments-api')) as HTMLSelectElement
    const inherit = within(select).getByRole('option', { name: 'Inherit (Prod)' })
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
    fireEvent.click(within(dialog).getByRole('radio', { name: 'Staging' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save tag' }))
    await waitFor(() => expect(bodies).toEqual([{ tag_id: STAGING_TAG_ID }]))
  })
})
