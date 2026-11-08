import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { ALL_TASKS, SERVICE_PAYMENTS, tasksList } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const panel = (name: string) => screen.getByRole('region', { name })

describe('DeployTab', () => {
  it('shows the four panels; Service and Auto scaling are editors seeded from deploy-settings', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/deploy`)

    await waitFor(() => within(panel('Service')).getByRole('spinbutton', { name: 'Desired count' }))
    await screen.findByRole('button', { name: 'Add variable' })
    for (const title of ['Containers', 'Task size', 'Service', 'Auto scaling']) {
      expect(screen.getByRole('heading', { name: title })).toBeInTheDocument()
    }
    const service = within(panel('Service'))
    expect(service.getByRole('spinbutton', { name: 'Desired count' })).toHaveValue(3)
    expect(service.getByRole('checkbox', { name: 'ECS Exec' })).toBeChecked()
    expect(service.getByRole('checkbox', { name: 'Circuit breaker' })).toBeChecked()

    const scaling = within(panel('Auto scaling'))
    expect(scaling.getByLabelText('Minimum')).toHaveValue(2)
    expect(scaling.getByLabelText('Maximum')).toHaveValue(8)
    expect(scaling.getByText('cpu-target-60')).toBeInTheDocument()
    expect(scaling.getByText('business-hours')).toBeInTheDocument()
    expect(screen.queryByText(/coming soon|later phase/i)).not.toBeInTheDocument()
  })

  it('editing the containers marks the definition changed; reverting the edit clears it', async () => {
    const user = userEvent.setup()
    const { container } = renderApp(`/services/${SERVICE_PAYMENTS.id}/deploy`)
    const grid = () => container.querySelector('[data-definition-changed]')!

    await screen.findByRole('button', { name: 'Add variable' })
    const containers = within(screen.getByRole('region', { name: 'Containers' }))
    await waitFor(() => expect(grid()).toHaveAttribute('data-definition-changed', 'false'))
    await user.click(await containers.findByRole('button', { name: 'Add variable' }))
    expect(grid()).toHaveAttribute('data-definition-changed', 'true')
    await user.click(containers.getByRole('button', { name: 'Remove variable 2' }))
    expect(grid()).toHaveAttribute('data-definition-changed', 'false')
  })

  it('shows the out-of-memory hint only when the service has an OOM stop', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/deploy`)
    await screen.findByRole('combobox', { name: 'Task memory' })
    const size = within(screen.getByRole('region', { name: 'Task size' }))
    expect(size.queryByText(/OutOfMemoryError/)).not.toBeInTheDocument()
  })

  it('shows the out-of-memory hint when a task stopped for OutOfMemoryError', async () => {
    const oomTask = { ...ALL_TASKS.find((task) => task.stopped_reason.includes('OutOfMemoryError'))! }
    server.use(http.get('/api/services/:id/tasks', () => HttpResponse.json({ ...tasksList([oomTask]), items: [oomTask] })))
    renderApp(`/services/${SERVICE_PAYMENTS.id}/deploy`)
    await screen.findByRole('combobox', { name: 'Task memory' })
    expect(await within(screen.getByRole('region', { name: 'Task size' })).findByText(/OutOfMemoryError .* at 1024 MiB/)).toBeInTheDocument()
  })
})
