import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { DeploymentDTO } from '../../../api/types'
import { RolloutCard } from './RolloutCard'

const NOW = new Date().toISOString()

function deployment(overrides: Partial<DeploymentDTO>): DeploymentDTO {
  return {
    id: 'ecs-svc/1',
    status: 'PRIMARY',
    revision: 10,
    desired_count: 3,
    running_count: 3,
    pending_count: 0,
    failed_tasks: 0,
    rollout_state: 'COMPLETED',
    rollout_state_reason: '',
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  }
}

const baseProps = {
  serviceName: 'payments-api',
  desiredCount: 3,
  strategyText: 'Rolling update · min healthy 100% · max 200% · circuit breaker with rollback is on',
  circuitBreakerEnable: true,
  circuitBreakerRollback: true,
}

describe('RolloutCard', () => {
  it('shows progress with new and old task counts', () => {
    const deployments = [
      deployment({ rollout_state: 'IN_PROGRESS', running_count: 2 }),
      deployment({ id: 'ecs-svc/0', status: 'ACTIVE', revision: 9, running_count: 1 }),
    ]
    render(<RolloutCard {...baseProps} deployments={deployments} />)
    expect(screen.getByText('Rollout in progress')).toBeInTheDocument()
    expect(screen.getByText(/:9 → :10/)).toBeInTheDocument()
    expect(screen.getByText('2 new tasks on :10 · 1 old on :9 · circuit breaker with rollback is on', { exact: false })).toBeInTheDocument()
  })

  it('says the breaker is on without rollback', () => {
    const deployments = [
      deployment({ rollout_state: 'IN_PROGRESS', running_count: 1 }),
      deployment({ id: 'ecs-svc/0', status: 'ACTIVE', revision: 9, running_count: 2 }),
    ]
    render(<RolloutCard {...baseProps} circuitBreakerRollback={false} deployments={deployments} />)
    expect(screen.getByText(/circuit breaker is on, without rollback/)).toBeInTheDocument()
  })

  it('falls back to created_at for the age and renders no revision without deployments', () => {
    const { container } = render(<RolloutCard {...baseProps} deployments={[]} />)
    expect(screen.queryByText(/payments-api:/)).not.toBeInTheDocument()
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the failed-to-start fragment in danger text', () => {
    const deployments = [
      deployment({ rollout_state: 'IN_PROGRESS', running_count: 1, failed_tasks: 2 }),
      deployment({ id: 'ecs-svc/0', status: 'ACTIVE', revision: 9, running_count: 2 }),
    ]
    render(<RolloutCard {...baseProps} circuitBreakerEnable={false} circuitBreakerRollback={false} deployments={deployments} />)
    expect(screen.getByText('2 failed to start')).toBeInTheDocument()
    expect(screen.getByText(/ circuit breaker is off$/)).toBeInTheDocument()
  })

  it('shows Completed with the service revision and strategy text', () => {
    render(<RolloutCard {...baseProps} deployments={[deployment({})]} />)
    expect(screen.getByText('Completed')).toBeInTheDocument()
    expect(screen.getByText('payments-api:10')).toBeInTheDocument()
    expect(screen.getByText(baseProps.strategyText)).toBeInTheDocument()
  })

  it('shows Failed with the rollout reason', () => {
    const deployments = [deployment({ rollout_state: 'FAILED', rollout_state_reason: 'ECS deployment circuit breaker: tasks failed to start' })]
    render(<RolloutCard {...baseProps} deployments={deployments} />)
    expect(screen.getByText('Failed')).toBeInTheDocument()
    expect(screen.getByText('ECS deployment circuit breaker: tasks failed to start')).toBeInTheDocument()
  })
})
