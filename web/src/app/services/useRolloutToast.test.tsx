import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { DeploymentDTO, ServiceDTO } from '../../api/types'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { ToastProvider } from '../../components/Toast'
import { useRolloutToast } from './useRolloutToast'

function dep(overrides: Partial<DeploymentDTO>): DeploymentDTO {
  return { ...SERVICE_PAYMENTS.deployments[0], ...overrides }
}

const svc = (deployments: DeploymentDTO[]): ServiceDTO => ({ ...SERVICE_PAYMENTS, name: 'payments-api', desired: 3, deployments })

function Probe({ service }: { service: ServiceDTO }) {
  useRolloutToast(service)
  return null
}

const inProgress = svc([
  dep({ id: 'd43', status: 'PRIMARY', revision: 43, rollout_state: 'IN_PROGRESS' }),
  dep({ id: 'd42', status: 'ACTIVE', revision: 42, rollout_state: 'COMPLETED' }),
])

describe('useRolloutToast', () => {
  it('toasts once when the PRIMARY rollout completes', () => {
    const { rerender } = render(<Probe service={inProgress} />, { wrapper: ToastProvider })
    const done = svc([dep({ id: 'd43', status: 'PRIMARY', revision: 43, rollout_state: 'COMPLETED' })])
    rerender(<Probe service={done} />)
    rerender(<Probe service={{ ...done }} />)
    expect(screen.getAllByText(/is running :43 on all 3 tasks\./)).toHaveLength(1)
    expect(screen.getByText('payments-api').tagName).toBe('STRONG')
  })

  it('does not toast on first render of a completed rollout', () => {
    render(<Probe service={svc([dep({ rollout_state: 'COMPLETED' })])} />, { wrapper: ToastProvider })
    expect(screen.queryByRole('status')?.textContent ?? '').toBe('')
  })

  it('toasts a rollback when the remembered deployment is ACTIVE+FAILED and a new PRIMARY exists', () => {
    const { rerender } = render(<Probe service={svc([
      dep({ id: 'd7', status: 'PRIMARY', revision: 7, rollout_state: 'IN_PROGRESS' }),
      dep({ id: 'd6', status: 'ACTIVE', revision: 6, rollout_state: 'COMPLETED' }),
    ])} />, { wrapper: ToastProvider })
    rerender(<Probe service={svc([
      dep({ id: 'd8', status: 'PRIMARY', revision: 6, rollout_state: 'IN_PROGRESS' }),
      dep({ id: 'd7', status: 'ACTIVE', revision: 7, rollout_state: 'FAILED' }),
    ])} />)
    expect(screen.getByText(/revision :7 failed to start\. Rolled back to :6\./)).toBeInTheDocument()
  })

  it('toasts a failure using the ACTIVE revision when the PRIMARY id is unchanged', () => {
    const { rerender } = render(<Probe service={inProgress} />, { wrapper: ToastProvider })
    rerender(<Probe service={svc([
      dep({ id: 'd43', status: 'PRIMARY', revision: 43, rollout_state: 'FAILED' }),
      dep({ id: 'd42', status: 'ACTIVE', revision: 42, rollout_state: 'COMPLETED' }),
    ])} />)
    expect(screen.getByText(/revision :43 failed to start\. Rolled back to :42\./)).toBeInTheDocument()
  })
})
