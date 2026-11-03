import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'

const panel = (name: string) => screen.getByRole('region', { name })

describe('DeployTab', () => {
  it('shows the four panels; Service and Auto scaling are editors seeded from deploy-settings', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/deploy`)

    for (const title of ['Containers', 'Task size', 'Service', 'Auto scaling']) {
      expect(await screen.findByRole('heading', { name: title })).toBeInTheDocument()
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

    const containers = within(await screen.findByRole('region', { name: 'Containers' }))
    await waitFor(() => expect(grid()).toHaveAttribute('data-definition-changed', 'false'))
    await user.click(await containers.findByRole('button', { name: 'Add variable' }))
    expect(grid()).toHaveAttribute('data-definition-changed', 'true')
    await user.click(containers.getByRole('button', { name: 'Remove variable 2' }))
    expect(grid()).toHaveAttribute('data-definition-changed', 'false')
  })
})
