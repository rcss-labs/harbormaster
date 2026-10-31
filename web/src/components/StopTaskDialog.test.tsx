import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/setup'
import { StopTaskDialog } from './StopTaskDialog'

const serviceProps = { taskId: 'task-1', shortId: 'a1b2c3', serviceName: 'payments-api', desiredCount: 3 }

describe('StopTaskDialog', () => {
  it('explains the replacement for a service task', () => {
    renderWithProviders(<StopTaskDialog {...serviceProps} onClose={vi.fn()} />)
    expect(screen.getByRole('heading', { name: 'Stop task a1b2c3?' })).toBeInTheDocument()
    expect(screen.getByText('payments-api will start a replacement to keep 3 running.')).toBeInTheDocument()
  })

  it('says nothing replaces a standalone task', () => {
    renderWithProviders(
      <StopTaskDialog taskId="task-2" shortId="d4e5f6" serviceName={null} desiredCount={null} onClose={vi.fn()} />,
    )
    expect(screen.getByRole('heading', { name: 'Stop task d4e5f6?' })).toBeInTheDocument()
    expect(screen.getByText('This task belongs to no service; nothing will replace it.')).toBeInTheDocument()
  })

  it('posts the stop request for the task and closes on success', async () => {
    const stopped: string[] = []
    server.use(
      http.post('/api/tasks/:id/stop', ({ params }) => {
        stopped.push(params.id as string)
        return HttpResponse.json({ task_arn: 'arn:task-1', reason: 'Stopped by Harbormaster' })
      }),
    )
    const onClose = vi.fn()
    renderWithProviders(<StopTaskDialog {...serviceProps} onClose={onClose} />)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Stop task' }))
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(stopped).toEqual(['task-1'])
  })

  it('shows the API error message in a toast and stays open', async () => {
    server.use(
      http.post('/api/tasks/:id/stop', () =>
        HttpResponse.json({ error: { code: 'conflict', message: 'Task is already stopping.' } }, { status: 409 }),
      ),
    )
    const onClose = vi.fn()
    renderWithProviders(<StopTaskDialog {...serviceProps} onClose={onClose} />)
    await userEvent.setup().click(screen.getByRole('button', { name: 'Stop task' }))
    expect(await screen.findByText('Task is already stopping.')).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
  })
})
