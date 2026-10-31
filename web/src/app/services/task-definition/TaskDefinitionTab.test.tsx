import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { ALL_TASKS, SERVICE_PAYMENTS, TASK_DEFINITION_EDITABLE, tasksList } from '../../../test/fixtures'
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
})
