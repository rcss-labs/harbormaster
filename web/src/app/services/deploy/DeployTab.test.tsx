import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'

const panel = (name: string) => screen.getByRole('region', { name })

describe('DeployTab', () => {
  it('shows the four panels with the current settings read from deploy-settings', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/deploy`)

    for (const title of ['Containers', 'Task size', 'Service', 'Auto scaling']) {
      expect(await screen.findByRole('heading', { name: title })).toBeInTheDocument()
    }
    const service = within(panel('Service'))
    expect(service.getByText('Desired count').nextElementSibling).toHaveTextContent('3')
    expect(service.getByText('ECS Exec').nextElementSibling).toHaveTextContent('On')
    expect(service.getByText('Deployment').nextElementSibling).toHaveTextContent('min healthy 100% · max 200% · circuit breaker with rollback')

    const scaling = within(panel('Auto scaling'))
    expect(scaling.getByText(/min 2 · max 8/)).toBeInTheDocument()
    expect(scaling.getByText(/1 policy · 1 scheduled action/)).toBeInTheDocument()
    expect(screen.queryByText(/coming soon|later phase/i)).not.toBeInTheDocument()
  })
})
