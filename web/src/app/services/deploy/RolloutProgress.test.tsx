import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { DeploymentDTO, ServiceDTO } from '../../../api/types'
import { ToastProvider } from '../../../components/Toast'
import { SERVICE_NOTIFICATIONS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { RolloutProgress, shouldShowProgress } from './RolloutProgress'

const NOW = Date.parse('2026-11-03T12:00:00Z')
const iso = (offsetSeconds: number) => new Date(NOW + offsetSeconds * 1000).toISOString()

function deployment(overrides: Partial<DeploymentDTO>): DeploymentDTO {
  return {
    id: 'ecs-svc/1',
    status: 'PRIMARY',
    revision: 10,
    desired_count: 3,
    running_count: 1,
    pending_count: 0,
    failed_tasks: 0,
    rollout_state: 'IN_PROGRESS',
    rollout_state_reason: '',
    created_at: iso(-120),
    updated_at: iso(-10),
    ...overrides,
  }
}

const serviceWith = (deployments: DeploymentDTO[]): ServiceDTO => ({ ...SERVICE_NOTIFICATIONS, desired: 3, deployments })
const rolling = serviceWith([
  deployment({ running_count: 1, failed_tasks: 2 }),
  deployment({ id: 'ecs-svc/0', status: 'ACTIVE', revision: 9, running_count: 2, rollout_state: 'COMPLETED' }),
])

function renderProgress(service: ServiceDTO, canDeploy = true) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <RolloutProgress serviceId={service.id} service={service} canDeploy={canDeploy} tagName="Prod" />
      </ToastProvider>
    </QueryClientProvider>,
  )
}

describe('RolloutProgress', () => {
  it('shows IN_PROGRESS counts and Roll back to the previous revision', () => {
    renderProgress(rolling)
    expect(screen.getByText('Rollout in progress')).toBeInTheDocument()
    expect(screen.getByText(/:9 → :10/)).toBeInTheDocument()
    expect(screen.getByText(/1 new task on :10/)).toBeInTheDocument()
    expect(screen.getByText(/2 old on :9/)).toBeInTheDocument()
    expect(screen.getByText(/2 failed to start/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Roll back to :9' })).toBeEnabled()
  })

  it('rollback confirm calls deploy with interrupt', async () => {
    let body: unknown
    server.use(
      http.post('/api/services/:id/deploy', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ deployment_id: 'd-1', started_at: iso(0) }, { status: 202 })
      }),
    )
    renderProgress(rolling)
    await userEvent.click(screen.getByRole('button', { name: 'Roll back to :9' }))
    const dialog = await screen.findByRole('dialog')
    expect(body).toBeUndefined()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Roll back to :9' }))
    await waitFor(() => expect(body).toEqual({ revision: 9, interrupt: true }))
  })

  it('hides rollback without Deploy', () => {
    renderProgress(rolling, false)
    expect(screen.getByText('Rollout in progress')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Roll back/ })).not.toBeInTheDocument()
  })

  it('offers no rollback once the rollout completed', () => {
    renderProgress(serviceWith([deployment({ rollout_state: 'COMPLETED', running_count: 3 })]))
    expect(screen.getByText('Completed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Roll back/ })).not.toBeInTheDocument()
  })
})

describe('shouldShowProgress', () => {
  it('running or completed within 60 s', () => {
    expect(shouldShowProgress(rolling, NOW)).toBe(true)
    const completed = (updatedSecondsAgo: number) =>
      serviceWith([deployment({ rollout_state: 'COMPLETED', updated_at: iso(-updatedSecondsAgo) })])
    expect(shouldShowProgress(completed(30), NOW)).toBe(true)
    expect(shouldShowProgress(completed(90), NOW)).toBe(false)
    expect(shouldShowProgress(serviceWith([]), NOW)).toBe(false)
  })
})
