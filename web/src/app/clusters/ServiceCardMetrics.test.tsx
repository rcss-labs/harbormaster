import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { ClusterMetricsDTO, ServiceRef } from '../../api/types'
import { ServiceCardMetrics } from './ServiceCardMetrics'

const SERVICE: ServiceRef = { id: 'svc-payments', name: 'payments-api', status: 'steady' }

type ServiceMetrics = ClusterMetricsDTO['services'][string]

const METRICS_WITH_ALB: ServiceMetrics = {
  cpu: [12, 18, 21],
  mem: [40, 42, 44],
  cpu_now: 21,
  mem_now: 44,
  alb: { req_now: 367, p95_now: 118.4, err5xx_today: 14 },
}

describe('ServiceCardMetrics', () => {
  it('shows ALB foot stats', () => {
    render(<ServiceCardMetrics service={SERVICE} m={METRICS_WITH_ALB} insights={false} />)

    const foot = screen.getByRole('group', { name: 'payments-api metrics' })
    expect(within(foot).getByText('367')).toBeInTheDocument()
    expect(within(foot).getByText('118')).toBeInTheDocument()
    expect(within(foot).getByText('14')).toBeInTheDocument()
    expect(foot).toHaveTextContent('req/min')
    expect(foot).toHaveTextContent('ms p95')
    expect(foot).toHaveTextContent('5xx today')
  })

  it('shows No load balancer', () => {
    render(<ServiceCardMetrics service={SERVICE} m={{ ...METRICS_WITH_ALB, alb: null }} insights={false} />)

    expect(screen.getByText('No load balancer')).toBeInTheDocument()
  })
})
