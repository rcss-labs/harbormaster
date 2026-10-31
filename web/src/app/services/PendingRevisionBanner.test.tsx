import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ControllerBanner } from './ControllerBanner'
import { PendingRevisionBanner } from './PendingRevisionBanner'

const props = { serviceName: 'payments-api', pendingRevision: 43, deployedRevision: 42, canDeploy: true, onDeploy: vi.fn() }

describe('PendingRevisionBanner', () => {
  it('explains the pending revision and offers Deploy', async () => {
    const onDeploy = vi.fn()
    render(<PendingRevisionBanner {...props} onDeploy={onDeploy} />)
    expect(screen.getByText(/is registered but not deployed\. The service still runs :42\./)).toHaveTextContent(
      'Revision payments-api:43 is registered but not deployed. The service still runs :42.',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Deploy :43' }))
    expect(onDeploy).toHaveBeenCalled()
  })

  it('disables Deploy with the lock title without permission', () => {
    render(<PendingRevisionBanner {...props} canDeploy={false} deployDisabledReason="You need Deploy on Prod" />)
    const button = screen.getByRole('button', { name: 'Deploy :43' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', 'You need Deploy on Prod')
  })
})

describe('ControllerBanner', () => {
  it('shows the banner text', () => {
    render(<ControllerBanner banner="This service uses CODE_DEPLOY, so Harbormaster cannot change it." />)
    expect(screen.getByText('This service uses CODE_DEPLOY, so Harbormaster cannot change it.')).toBeInTheDocument()
  })
})
