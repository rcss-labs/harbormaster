import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { ALL_TASKS, REVISIONS_PAYMENTS, SERVICE_PAYMENTS, TASK_DEFINITION_EDITABLE, tasksList } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const route = `/services/${SERVICE_PAYMENTS.id}/task-definition`
const result = (canDeploy: boolean) => ({
  revision: 43,
  arn: 'arn:aws:ecs:us-east-1:1:task-definition/payments-api:43',
  image_tag: '1.18.2',
  cpu: '512',
  memory: '1024',
  can_deploy: canDeploy,
  tag: { id: 3, name: 'Prod', color: 'red', inherited: true },
  strategy: 'Rolling update',
})

async function editEnvValue() {
  const editor = await screen.findByRole('textbox', { name: 'Task definition JSON' })
  const value = await screen.findByRole('textbox', { name: 'Value' })
  await userEvent.clear(value)
  await userEvent.type(value, 'debug')
  return editor
}

describe('TaskDefinitionTab', () => {
  it('renders the JSON with family first', async () => {
    renderApp(route)
    const editor = (await screen.findByRole('textbox', { name: 'Task definition JSON' })) as HTMLTextAreaElement
    expect(editor.value.startsWith('{\n  "family": "payments-api"')).toBe(true)
    expect(screen.getByText('No changes')).toBeInTheDocument()
  })

  it('pauses the form on invalid JSON and disables review', async () => {
    renderApp(route)
    const editor = await screen.findByRole('textbox', { name: 'Task definition JSON' })
    await userEvent.type(editor, '{{')
    expect(await screen.findByText(/The JSON has an error, so the form is paused:/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Review and save' })).toBeDisabled()
  })

  it('is read only for a viewer', async () => {
    server.use(
      http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, edit: false } })),
    )
    renderApp(route)
    const editor = await screen.findByRole('textbox', { name: 'Task definition JSON' })
    expect(editor).toHaveAttribute('readonly')
    expect(screen.getByText(/Read only\. You need/)).toBeInTheDocument()
  })

  it('opens the review dialog after an edit', async () => {
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    expect(await screen.findByRole('dialog')).toHaveTextContent('Register payments-api:44?')
  })

  it('reopens the review with the stale banner after a 409', async () => {
    let reads = 0
    server.use(
      http.get('/api/services/:id/task-definition', () => {
        reads++
        return HttpResponse.json({ ...TASK_DEFINITION_EDITABLE, deployed_revision: reads > 1 ? 43 : 42 })
      }),
      http.post('/api/services/:id/revisions', () =>
        HttpResponse.json({ error: { code: 'conflict', message: 'stale' }, deployed_revision: 43 }, { status: 409 }),
      ),
    )
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText(/The service moved from :42 to :43/)).toBeInTheDocument()
  })

  it('toasts when the user cannot deploy', async () => {
    server.use(http.post('/api/services/:id/revisions', () => HttpResponse.json(result(false), { status: 201 })))
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText('Registered. Someone with Deploy on Prod can roll it out.')).toBeInTheDocument()
  })

  it('shows the recording warning from a register response as a toast', async () => {
    server.use(
      http.post('/api/services/:id/revisions', () =>
        HttpResponse.json({ ...result(false), warning: 'Registered, but Harbormaster could not record it.' }, { status: 201 }),
      ),
    )
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText('Registered, but Harbormaster could not record it.')).toBeInTheDocument()
  })

  it('shows the recording warning from a deploy response as a toast', async () => {
    server.use(
      http.post('/api/services/:id/deploy', () =>
        HttpResponse.json({ deployment_id: 'd-1', warning: 'Rolled out, but Harbormaster could not record it in the operations history.' }, { status: 202 }),
      ),
    )
    renderApp(route)
    await userEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Deploy :43' }))
    expect(await screen.findByText('Rolled out, but Harbormaster could not record it in the operations history.')).toBeInTheDocument()
  })

  it('asks to deploy when the user can', async () => {
    server.use(http.post('/api/services/:id/revisions', () => HttpResponse.json(result(true), { status: 201 })))
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByRole('heading', { name: 'Revision :43 is registered. Deploy it now?' })).toBeInTheDocument()
  })

  it('shows the out of memory hint from stopped tasks', async () => {
    const oomTask = ALL_TASKS.find((task) => task.stopped_reason.includes('OutOfMemoryError'))!
    server.use(http.get('/api/services/:id/tasks', () => HttpResponse.json(tasksList([oomTask]))))
    renderApp(route)
    expect(await screen.findByText('Tasks of this service stop with OutOfMemoryError (exit 137) at 1024 MiB. Try 2048 MiB.')).toBeInTheDocument()
  })

  it('blocks a bad free-text tag when ECR is not used and sends nothing', async () => {
    let posts = 0
    server.use(
      http.get('/api/services/:id/image-tags', () => HttpResponse.json({ source: 'none', repository: '', current: '', tags: [] })),
      http.post('/api/services/:id/revisions', () => {
        posts++
        return HttpResponse.json(result(true), { status: 201 })
      }),
    )
    renderApp(route)
    const tag = await screen.findByRole('textbox', { name: 'Image tag' })
    await userEvent.clear(tag)
    await userEvent.type(tag, 'bad tag!')
    expect(screen.getByText('Tag may contain letters, digits, . _ -')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Review and save' })).toBeDisabled()
    expect(posts).toBe(0)
  })

  it('does not block review for an untagged image when the tag is not edited', async () => {
    server.use(
      http.get('/api/services/:id/image-tags', () => HttpResponse.json({ source: 'none', repository: '', current: '', tags: [] })),
      http.get('/api/services/:id/task-definition', () =>
        HttpResponse.json({
          ...TASK_DEFINITION_EDITABLE,
          definition: {
            ...TASK_DEFINITION_EDITABLE.definition,
            containerDefinitions: [{ name: 'payments-api', image: 'payments-api', essential: true, environment: [{ name: 'LOG_LEVEL', value: 'info' }] }],
          },
        }),
      ),
    )
    renderApp(route)
    await editEnvValue()
    expect(screen.getByRole('button', { name: 'Review and save' })).toBeEnabled()
  })

  it('does not block review while image tags are still loading', async () => {
    server.use(http.get('/api/services/:id/image-tags', () => new Promise(() => {})))
    renderApp(route)
    await editEnvValue()
    expect(screen.getByRole('button', { name: 'Review and save' })).toBeEnabled()
  })

  it('shows a 422 message in the review dialog', async () => {
    server.use(
      http.post('/api/services/:id/revisions', () =>
        HttpResponse.json({ error: { code: 'invalid', message: 'Changing roles needs a global admin' } }, { status: 422 }),
      ),
    )
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText('Changing roles needs a global admin')).toBeInTheDocument()
  })

  it('shows an error when the refetch after a 409 fails', async () => {
    let reads = 0
    server.use(
      http.get('/api/services/:id/task-definition', () => {
        reads++
        return reads > 1
          ? HttpResponse.json({ error: { code: 'internal', message: 'Refetch failed badly' } }, { status: 500 })
          : HttpResponse.json(TASK_DEFINITION_EDITABLE)
      }),
      http.post('/api/services/:id/revisions', () =>
        HttpResponse.json({ error: { code: 'conflict', message: 'stale' }, deployed_revision: 43 }, { status: 409 }),
      ),
    )
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText(/Refetch failed badly/)).toBeInTheDocument()
  })

  it('toasts on Not now', async () => {
    server.use(http.post('/api/services/:id/revisions', () => HttpResponse.json(result(true), { status: 201 })))
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    await userEvent.click(await screen.findByRole('button', { name: 'Not now' }))
    expect(await screen.findByText('payments-api:43 is registered. Deploy it from the service page when you are ready.')).toBeInTheDocument()
  })

  it('toasts when a rollout is already running', async () => {
    server.use(
      http.post('/api/services/:id/revisions', () => HttpResponse.json(result(true), { status: 201 })),
      http.post('/api/services/:id/deploy', () => HttpResponse.json({ error: { code: 'conflict', message: 'running' } }, { status: 409 })),
    )
    renderApp(route)
    await editEnvValue()
    await userEvent.click(screen.getByRole('button', { name: 'Review and save' }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Deploy :43' }))
    expect(await screen.findByText('A rollout is already running')).toBeInTheDocument()
  })

  it('drops the draft when the text returns to the deployed definition', async () => {
    renderApp(route)
    const value = await screen.findByRole('textbox', { name: 'Value' })
    await userEvent.type(value, 'x')
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    await userEvent.type(value, '{Backspace}')
    expect(screen.getByText('No changes')).toBeInTheDocument()
    expect(screen.getByText('based on :42')).toBeInTheDocument()
  })
  // ---- Revisions table wiring (task 16) ----
  it('deploys a not-deployed revision from the table and opens the Overview tab', async () => {
    let body: unknown
    server.use(
      http.post('/api/services/:id/deploy', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ deployment_id: 'd-1', started_at: '2026-10-31T10:00:00Z' }, { status: 202 })
      }),
    )
    renderApp(route)
    await userEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('Deploy payments-api to :43?')
    expect(dialog).toHaveTextContent('1.18.3')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Deploy :43' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/overview`))
    expect(body).toEqual({ revision: 43 })
  })

  it('opens the rollback dialog for a previous revision', async () => {
    server.use(
      http.get('/api/services/:id/revisions', () =>
        HttpResponse.json({
          ...REVISIONS_PAYMENTS,
          items: [REVISIONS_PAYMENTS.items[1], { ...REVISIONS_PAYMENTS.items[1], revision: 41, status: 'previous', image_tag: '1.18.1' }],
        }),
      ),
    )
    renderApp(route)
    await userEvent.click(await screen.findByRole('button', { name: 'Roll back to this' }))
    expect(await screen.findByRole('dialog')).toHaveTextContent('Roll back payments-api to :41?')
  })

  it('opens the deploy dialog straight away for ?deploy=N', async () => {
    renderApp(`${route}?deploy=43`)
    expect(await screen.findByRole('dialog')).toHaveTextContent('Deploy payments-api to :43?')
  })
  it('ignores ?deploy=N without Deploy permission and clears the param', async () => {
    server.use(http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, deploy: false } })))
    renderApp(`${route}?deploy=43`)
    await screen.findByRole('heading', { name: 'Revisions' })
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('clears ?deploy=N when the revision is unknown or already deployed', async () => {
    renderApp(`${route}?deploy=999`)
    await screen.findByRole('heading', { name: 'Revisions' })
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('uses rollback mode for ?deploy=N on a previous revision', async () => {
    server.use(
      http.get('/api/services/:id/revisions', () =>
        HttpResponse.json({ ...REVISIONS_PAYMENTS, items: [REVISIONS_PAYMENTS.items[1], { ...REVISIONS_PAYMENTS.items[1], revision: 41, status: 'previous' }] }),
      ),
    )
    renderApp(`${route}?deploy=41`)
    expect(await screen.findByRole('dialog')).toHaveTextContent('Roll back payments-api to :41?')
  })

  it('titles table actions with the controller banner when the controller is unsupported', async () => {
    server.use(
      http.get('/api/services/:id', () =>
        HttpResponse.json({ ...SERVICE_PAYMENTS, controller: { type: 'CODE_DEPLOY', supported: false, name: '', banner: 'This service uses CODE_DEPLOY.' } }),
      ),
    )
    renderApp(route)
    const button = await screen.findByRole('button', { name: 'Deploy' })
    await waitFor(() => expect(button).toBeDisabled())
    expect(button).toHaveAttribute('title', 'This service uses CODE_DEPLOY.')
  })
})
