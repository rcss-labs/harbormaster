import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { REVISIONS_PAYMENTS, SERVICE_PAYMENTS, TASK_DEFINITION_EDITABLE } from '../../../test/fixtures'
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

const JSON_EDITOR = { name: 'Task definition JSON' }

/** Edits the JSON text directly: the Deploy tab owns the form, this tab only has the editor. */
async function editLogLevel(to = 'debug', from = 'info') {
  const editor = (await screen.findByRole('textbox', JSON_EDITOR)) as HTMLTextAreaElement
  fireEvent.change(editor, { target: { value: editor.value.replace(`"${from}"`, `"${to}"`) } })
  return editor
}

describe('TaskDefinitionTab', () => {
  it('renders the JSON with family first', async () => {
    renderApp(route)
    const editor = (await screen.findByRole('textbox', { name: 'Task definition JSON' })) as HTMLTextAreaElement
    expect(editor.value.startsWith('{\n  "family": "payments-api"')).toBe(true)
    expect(screen.getByText('No changes')).toBeInTheDocument()
  })

  it('a failed read shows a panel error with Retry, and Retry loads the editor', async () => {
    let calls = 0
    server.use(
      http.get('/api/services/:id/task-definition', () => {
        calls += 1
        return calls === 1
          ? HttpResponse.json({ error: { code: 'aws_error', message: 'ECS unavailable' } }, { status: 502 })
          : HttpResponse.json(TASK_DEFINITION_EDITABLE)
      }),
    )
    renderApp(route)
    expect(await screen.findByRole('alert')).toHaveTextContent('The task definition could not be read.')
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('textbox', JSON_EDITOR)).toBeInTheDocument()
  })

  it('disables review on invalid JSON', async () => {
    renderApp(route)
    const editor = await screen.findByRole('textbox', { name: 'Task definition JSON' })
    await userEvent.type(editor, '{{')
    expect(screen.getByRole('button', { name: /^Review and register :\d+$/ })).toBeDisabled()
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
    await editLogLevel()
    await userEvent.click(screen.getByRole('button', { name: /^Review and register :\d+$/ }))
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
    await editLogLevel()
    await userEvent.click(screen.getByRole('button', { name: /^Review and register :\d+$/ }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText(/The service moved from :42 to :43/)).toBeInTheDocument()
  })

  it('registering shows a toast and no deploy prompt', async () => {
    server.use(http.post('/api/services/:id/revisions', () => HttpResponse.json(result(true), { status: 201 })))
    renderApp(route)
    await editLogLevel()
    await userEvent.click(screen.getByRole('button', { name: /^Review and register :\d+$/ }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText('payments-api:43 is registered. Deploy it from the Deploy tab when you are ready.')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /Deploy it now/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('shows the recording warning from a register response as a toast', async () => {
    server.use(
      http.post('/api/services/:id/revisions', () =>
        HttpResponse.json({ ...result(false), warning: 'Registered, but Harbormaster could not record it.' }, { status: 201 }),
      ),
    )
    renderApp(route)
    await editLogLevel()
    await userEvent.click(screen.getByRole('button', { name: /^Review and register :\d+$/ }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText('Registered, but Harbormaster could not record it.')).toBeInTheDocument()
  })

  it('shows a 422 message in the review dialog', async () => {
    server.use(
      http.post('/api/services/:id/revisions', () =>
        HttpResponse.json({ error: { code: 'invalid', message: 'Changing roles needs a global admin' } }, { status: 422 }),
      ),
    )
    renderApp(route)
    await editLogLevel()
    await userEvent.click(screen.getByRole('button', { name: /^Review and register :\d+$/ }))
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
    await editLogLevel()
    await userEvent.click(screen.getByRole('button', { name: /^Review and register :\d+$/ }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))
    expect(await screen.findByText(/Refetch failed badly/)).toBeInTheDocument()
  })

  it('drops the draft when the text returns to the deployed definition', async () => {
    renderApp(route)
    await editLogLevel()
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    await editLogLevel('info', 'debug')
    expect(screen.getByText('No changes')).toBeInTheDocument()
    expect(screen.getByText('based on :42')).toBeInTheDocument()
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

  it('table Deploy navigates to /deploy?revision=', async () => {
    renderApp(route)
    await userEvent.click(await screen.findByRole('button', { name: 'Deploy' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/deploy`))
    expect(screen.getByTestId('search')).toHaveTextContent('revision=43')
  })

  it('table Roll back navigates to /deploy?revision=', async () => {
    server.use(
      http.get('/api/services/:id/revisions', () =>
        HttpResponse.json({ ...REVISIONS_PAYMENTS, items: [REVISIONS_PAYMENTS.items[1], { ...REVISIONS_PAYMENTS.items[1], revision: 41, status: 'previous' }] }),
      ),
    )
    renderApp(route)
    await userEvent.click(await screen.findByRole('button', { name: 'Roll back to this' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/deploy`))
    expect(screen.getByTestId('search')).toHaveTextContent('revision=41')
  })
})
