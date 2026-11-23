import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { Arm64Readiness, ChangeSetRequest } from '../../../api/deploy'
import { ARM64_READINESS_BLOCKED, CHANGESET_APPLIED, SERVICE_PAYMENTS, TASK_DEFINITION_EDITABLE } from '../../../test/fixtures'
import { imageTagsHandler } from '../../../test/imageTags'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const route = `/services/${SERVICE_PAYMENTS.id}/deploy`
const PAYMENTS_IMAGE = '482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.2'

const pendingRegion = () => within(screen.getByRole('region', { name: 'Pending changes' }))
const archSelect = () => screen.findByRole('combobox', { name: 'CPU architecture' })

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

const serveReadiness = (readiness: Arm64Readiness) => server.use(http.get('/api/services/:id/arm64-readiness', () => HttpResponse.json(readiness)))
const serveDefinition = (definition: Record<string, unknown>) =>
  server.use(http.get('/api/services/:id/task-definition', () => HttpResponse.json({ ...TASK_DEFINITION_EDITABLE, definition })))

describe('DeployTab: ARM64', () => {
  it('fills ARM64 from the link and uses the ARM64 reason', async () => {
    const user = userEvent.setup()
    const bodies = captureChangeSets()
    renderApp(`${route}?arch=ARM64`)

    expect(await archSelect()).toHaveValue('ARM64')
    const pending = pendingRegion()
    expect(await pending.findByRole('listitem', { name: 'CPU architecture' })).toHaveTextContent('X86_64 → ARM64')
    expect(pending.getByText('Move to ARM64 (Graviton) is suggested from Cost')).toBeInTheDocument()
    expect(bodies).toHaveLength(0)

    await user.click(pending.getByRole('button', { name: 'Review…' }))
    const dialog = within(screen.getByRole('dialog'))
    expect(dialog.getByRole('textbox', { name: 'Reason' })).toHaveValue('Move to ARM64 (Graviton)')
    await user.click(dialog.getByRole('button', { name: /^(Apply|Deploy|Register)/ }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].reason).toBe('Move to ARM64 (Graviton)')
    const sent = (bodies[0].task_definition as { definition: Record<string, unknown> }).definition
    expect(sent.runtimePlatform).toEqual({ cpuArchitecture: 'ARM64', operatingSystemFamily: 'LINUX' })
  })

  it('combines the rightsizing and ARM64 links', async () => {
    const user = userEvent.setup()
    renderApp(`${route}?arch=ARM64&task_cpu=256&task_memory=512&headroom=30`)
    expect(await archSelect()).toHaveValue('ARM64')
    expect(await screen.findByRole('combobox', { name: 'Task CPU' })).toHaveValue('256')
    await pendingRegion().findByRole('listitem', { name: 'Task CPU' })
    await user.click(pendingRegion().getByRole('button', { name: 'Review…' }))
    expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: 'Reason' })).toHaveValue('Rightsizing at 30% headroom; Move to ARM64 (Graviton)')
  })

  it('ignores the link without Edit', async () => {
    server.use(http.get('/api/services/:id/task-definition', () => HttpResponse.json({ ...TASK_DEFINITION_EDITABLE, can: { ...TASK_DEFINITION_EDITABLE.can, edit: false } })))
    renderApp(`${route}?arch=ARM64`)
    expect(await archSelect()).toHaveValue('X86_64')
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
  })

  it.each([
    ['an EC2', { requiresCompatibilities: ['EC2'] }],
    ['a Windows', { runtimePlatform: { operatingSystemFamily: 'WINDOWS_SERVER_2022_CORE', cpuArchitecture: 'X86_64' } }],
  ])('ignores the link for %s definition', async (_name, patch) => {
    serveDefinition({ ...TASK_DEFINITION_EDITABLE.definition, ...patch })
    renderApp(`${route}?arch=ARM64`)
    await screen.findByRole('combobox', { name: 'Task CPU' }) // the definition has loaded
    expect(screen.queryByRole('combobox', { name: 'CPU architecture' })).not.toBeInTheDocument()
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
  })

  it('lets the revision parameter win over the link', async () => {
    renderApp(`${route}?revision=41&arch=ARM64`)
    expect(await archSelect()).toHaveValue('X86_64')
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
  })

  it('says the service already runs on ARM64', async () => {
    serveDefinition({ ...TASK_DEFINITION_EDITABLE.definition, runtimePlatform: { cpuArchitecture: 'ARM64', operatingSystemFamily: 'LINUX' } })
    renderApp(`${route}?arch=ARM64`)
    expect(await archSelect()).toHaveValue('ARM64')
    expect(await pendingRegion().findByText('This service already runs on ARM64.')).toBeInTheDocument()
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
  })

  it('warns about the image without an arm64 build', async () => {
    serveReadiness(ARM64_READINESS_BLOCKED)
    renderApp(`${route}?arch=ARM64`)
    await archSelect()
    expect(await screen.findByText(`${PAYMENTS_IMAGE} is a single-architecture image`)).toBeInTheDocument()
    expect(screen.getByText('Tasks will fail to start on ARM64 until this image has an arm64 build.')).toBeInTheDocument()
    // The warning never stops Review: the cache can be a day old.
    expect(pendingRegion().getByRole('button', { name: 'Review…' })).toBeEnabled()
  })

  it.each([
    ['no_arm64_in_index', 'no_arm64', '', `${PAYMENTS_IMAGE} has no arm64 build`],
    ['pending', 'unchecked', '', `${PAYMENTS_IMAGE} has not been checked yet`],
    ['registry_not_checked', 'unchecked', '', `${PAYMENTS_IMAGE} is in a registry Harbormaster does not check`],
    ['no_permission', 'unchecked', 'sandbox-admin', 'The harbormaster-access role in sandbox-admin cannot read image manifests. Update the access module to the latest version.'],
    ['not_connected', 'unchecked', 'staging-example', `Connect AWS account staging-example to check ${PAYMENTS_IMAGE}`],
    ['not_found', 'unchecked', '', `${PAYMENTS_IMAGE} was not found`],
    ['denied', 'unchecked', '', `${PAYMENTS_IMAGE} could not be read`],
    ['error', 'unchecked', '', `${PAYMENTS_IMAGE} could not be read`],
  ] as const)('words the %s blocker', async (reason, state, account, text) => {
    serveReadiness({
      status: state === 'no_arm64' ? 'blocked' : 'unknown',
      images: [{ container: 'payments-api', image: PAYMENTS_IMAGE, state, reason, account }],
      checked_at: null,
    })
    renderApp(`${route}?arch=ARM64`)
    await archSelect()
    expect(await screen.findByText(text)).toBeInTheDocument()
    if (state === 'unchecked') expect(screen.queryByText('Tasks will fail to start on ARM64 until this image has an arm64 build.')).not.toBeInTheDocument()
  })

  it('shows nothing when every image has an arm64 build', async () => {
    serveReadiness({ status: 'ready', images: [{ container: 'payments-api', image: PAYMENTS_IMAGE, state: 'arm64', reason: '', account: '' }], checked_at: null })
    renderApp(`${route}?arch=ARM64`)
    await archSelect()
    await pendingRegion().findByRole('listitem', { name: 'CPU architecture' })
    await waitFor(() => expect(screen.queryByText(/single-architecture|has no arm64 build/)).not.toBeInTheDocument())
  })

  it('does not ask for readiness while the draft is not ARM64', async () => {
    let asked = 0
    server.use(http.get('/api/services/:id/arm64-readiness', () => { asked += 1; return HttpResponse.json(ARM64_READINESS_BLOCKED) }))
    renderApp(route)
    await archSelect()
    await screen.findByRole('button', { name: 'Add variable' })
    expect(asked).toBe(0)
  })

  it('says changed tags are not checked', async () => {
    const user = userEvent.setup()
    server.use(imageTagsHandler())
    renderApp(`${route}?arch=ARM64`)
    await archSelect()
    expect(screen.queryByText('Changed image tags are not checked for arm64 until they are deployed.')).not.toBeInTheDocument()
    await user.click(await screen.findByRole('combobox', { name: 'Image tag' }))
    await user.click(await screen.findByRole('option', { name: /1\.18\.4/ }))
    expect(await screen.findByText('Changed image tags are not checked for arm64 until they are deployed.')).toBeInTheDocument()
  })

  it('shows the architecture note and returns to no change when x86 is chosen again', async () => {
    const user = userEvent.setup()
    renderApp(`${route}?arch=ARM64`)
    expect(await screen.findByText('ARM64 tasks cost about 20% less. Every image must have an arm64 build.')).toBeInTheDocument()
    await user.selectOptions(await archSelect(), 'X86_64')
    await waitFor(() => expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0))
  })

  it.each([
    [
      'apply',
      async (user: ReturnType<typeof userEvent.setup>) => {
        await user.click(pendingRegion().getByRole('button', { name: 'Review…' }))
        await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: /^(Apply|Deploy|Register)/ }))
      },
    ],
    [
      'discard',
      async (user: ReturnType<typeof userEvent.setup>) => {
        await user.click(pendingRegion().getByRole('button', { name: 'Discard' }))
      },
    ],
  ])('clears the arch parameter after %s', async (_name, finish) => {
    const user = userEvent.setup()
    captureChangeSets()
    renderApp(`${route}?arch=ARM64`)
    await archSelect()
    await pendingRegion().findByRole('listitem', { name: 'CPU architecture' })
    await finish(user)
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
    expect(await archSelect()).toHaveValue('X86_64')
    await waitFor(() => expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0))
  })
})
