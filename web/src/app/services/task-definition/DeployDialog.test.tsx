import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { DeployDialog } from './DeployDialog'

const props = {
  mode: 'prompt' as const,
  serviceName: 'payments-api',
  clusterName: 'prod-main',
  revision: 43,
  deployedRevision: 42,
  runningCount: 3,
  imageTag: '1.18.3',
  cpu: '512',
  memory: '1024',
  strategy: 'Rolling update',
  effectiveTag: 'Prod',
  tagInherited: true,
  busy: false,
  onDismiss: vi.fn(),
  onConfirm: vi.fn(),
}

describe('DeployDialog', () => {
  it('shows the Prod banner only for Prod', () => {
    const { rerender } = render(<DeployDialog {...props} />)
    expect(screen.getByText('This is a Prod service.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Revision :43 is registered. Deploy it now?' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Not now' })).toBeInTheDocument()
    rerender(<DeployDialog {...props} effectiveTag="Dev" />)
    expect(screen.queryByText('This is a Prod service.')).not.toBeInTheDocument()
  })

  it('shows the image tag alone', () => {
    render(<DeployDialog {...props} />)
    expect(screen.getByText('1.18.3')).toBeInTheDocument()
    expect(screen.queryByText('payments-api:1.18.3')).not.toBeInTheDocument()
  })

  it('labels the rollback button', () => {
    render(<DeployDialog {...props} mode="rollback" revision={41} />)
    expect(screen.getByRole('button', { name: 'Roll back to :41' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
  })
})
