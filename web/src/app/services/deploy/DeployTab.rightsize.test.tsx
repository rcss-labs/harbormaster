import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { ChangeSetRequest } from '../../../api/deploy'
import { CHANGESET_APPLIED, SERVICE_PAYMENTS, TASK_DEFINITION_EDITABLE } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const route = `/services/${SERVICE_PAYMENTS.id}/deploy`
const SUGGESTION = 'task_cpu=256&task_memory=512&headroom=30'

const captureChangeSets = () => {
  const bodies: ChangeSetRequest[] = []
  server.use(
    http.post('/api/services/:id/changeset', async ({ request }) => {
      bodies.push((await request.json()) as ChangeSetRequest)
      return HttpResponse.json(CHANGESET_APPLIED)
    }),
  )
  return bodies
}

const pendingRegion = () => within(screen.getByRole('region', { name: 'Pending changes' }))
const cpuSelect = () => screen.findByRole('combobox', { name: 'Task CPU' })
const memorySelect = () => screen.getByRole('combobox', { name: 'Task memory' })

describe('DeployTab: a suggested task size from the Cost tab', () => {
  it('applies the suggestion on top of the live definition', async () => {
    const bodies = captureChangeSets()
    renderApp(`${route}?${SUGGESTION}`)

    expect(await cpuSelect()).toHaveValue('256')
    await waitFor(() => expect(memorySelect()).toHaveValue('512'))
    const pending = pendingRegion()
    expect(await pending.findByRole('listitem', { name: 'Task CPU' })).toHaveTextContent('512 units → 256 units')
    expect(pending.getByRole('listitem', { name: 'Task memory' })).toHaveTextContent('1024 MiB → 512 MiB')
    expect(pending.getByText('Task size 256 / 512 is suggested from Cost')).toBeInTheDocument()
    expect(bodies).toHaveLength(0)
  })

  it('sends the suggestion only after review and apply', async () => {
    const user = userEvent.setup()
    const bodies = captureChangeSets()
    renderApp(`${route}?${SUGGESTION}`)
    await cpuSelect()
    await pendingRegion().findByRole('listitem', { name: 'Task CPU' })

    await user.click(pendingRegion().getByRole('button', { name: 'Review…' }))
    const dialog = within(screen.getByRole('dialog'))
    expect(dialog.getByRole('textbox', { name: 'Reason' })).toHaveValue('Rightsizing at 30% headroom')
    expect(bodies).toHaveLength(0)
    await user.click(dialog.getByRole('button', { name: /^(Apply|Deploy|Register)/ }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].reason).toBe('Rightsizing at 30% headroom')
    expect(bodies[0].base_revision).toBe(42)
    const sent = (bodies[0].task_definition as { definition: Record<string, unknown> }).definition
    expect(sent.cpu).toBe('256')
    expect(sent.memory).toBe('512')
  })

  it('says the service already has this size', async () => {
    renderApp(`${route}?task_cpu=512&task_memory=1024&headroom=30`)
    await cpuSelect()
    expect(await pendingRegion().findByText('This service already has this size.')).toBeInTheDocument()
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
  })

  it('ignores an invalid size in the URL', async () => {
    renderApp(`${route}?task_cpu=512&task_memory=512&headroom=30`)
    expect(await cpuSelect()).toHaveValue('512')
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
    expect(pendingRegion().queryByText('This service already has this size.')).not.toBeInTheDocument()
  })

  it('ignores the suggestion without Edit', async () => {
    server.use(http.get('/api/services/:id/task-definition', () => HttpResponse.json({ ...TASK_DEFINITION_EDITABLE, can: { ...TASK_DEFINITION_EDITABLE.can, edit: false } })))
    renderApp(`${route}?${SUGGESTION}`)
    expect(await cpuSelect()).toHaveValue('512')
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
  })

  it('lets the revision parameter win over a suggestion', async () => {
    renderApp(`${route}?revision=41&${SUGGESTION}`)
    await cpuSelect()
    expect(await pendingRegion().findByText(':41 is loaded from the revisions list')).toBeInTheDocument()
    expect(await cpuSelect()).toHaveValue('512')
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
    expect(pendingRegion().queryByRole('listitem', { name: 'Task CPU' })).not.toBeInTheDocument()
  })

  it('blocks review when the suggestion is below the container floor', async () => {
    const reserved = { ...TASK_DEFINITION_EDITABLE.definition, containerDefinitions: [{ name: 'payments-api', image: 'payments-api:1.18.2', essential: true, memory: 900 }] }
    server.use(http.get('/api/services/:id/task-definition', () => HttpResponse.json({ ...TASK_DEFINITION_EDITABLE, definition: reserved })))
    renderApp(`${route}?${SUGGESTION}`)
    await cpuSelect()

    expect(await pendingRegion().findByText('Task memory 512 MiB is below the 900 MiB its containers reserve.')).toBeInTheDocument()
    expect(pendingRegion().getByRole('button', { name: 'Review…' })).toBeDisabled()
  })

  it.each([
    ['apply', async (user: ReturnType<typeof userEvent.setup>) => {
      await user.click(pendingRegion().getByRole('button', { name: 'Review…' }))
      await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: /^(Apply|Deploy|Register)/ }))
    }],
    ['discard', async (user: ReturnType<typeof userEvent.setup>) => {
      await user.click(pendingRegion().getByRole('button', { name: 'Discard' }))
    }],
  ])('clears the suggestion after %s', async (_name, finish) => {
    const user = userEvent.setup()
    captureChangeSets()
    renderApp(`${route}?${SUGGESTION}`)
    await cpuSelect()
    await pendingRegion().findByRole('listitem', { name: 'Task CPU' })

    await finish(user)

    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
    expect(await cpuSelect()).toHaveValue('512')
    await waitFor(() => expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0))
  })
})
