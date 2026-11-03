import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { withCan, withoutLoadBalancer, withoutTaskRole } from '../../../test/deployVariants'
import { ServicePanel } from './ServicePanel'
import { serviceDraftFromSettings, serviceDraftToChange, type ServiceDraft } from './serviceDraft'

let latestDraft: ServiceDraft

function Harness({ settings }: { settings: DeploySettingsResponse }) {
  const [draft, setDraft] = useState(() => (latestDraft = serviceDraftFromSettings(settings)))
  return <ServicePanel settings={settings} draft={draft} onChange={(next) => {
        latestDraft = next
        setDraft(next)
      }} tagName="prod" />
}

const execNote = "Applies to new tasks only: turning it on or off restarts the service's tasks"

describe('ServicePanel', () => {
  it('rollback cannot be on while the breaker is off', async () => {
    const user = userEvent.setup()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)
    const breaker = screen.getByRole('checkbox', { name: 'Circuit breaker' })
    const rollback = screen.getByRole('checkbox', { name: 'Roll back on failure' })
    expect(breaker).toBeChecked()
    expect(rollback).toBeChecked()
    expect(rollback).toBeEnabled()

    await user.click(breaker)
    expect(breaker).not.toBeChecked()
    expect(rollback).not.toBeChecked()
    expect(rollback).toBeDisabled()
    expect(latestDraft.circuitBreakerRollback).toBe(false)
    expect(serviceDraftToChange(DEPLOY_SETTINGS_PAYMENTS, latestDraft)?.deployment_configuration?.circuit_breaker).toEqual({ enable: false, rollback: false })
  })

  it('grace period disabled without a load balancer', () => {
    const { unmount } = render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)
    expect(screen.getByLabelText('Health check grace period (seconds)')).toBeEnabled()
    unmount()

    render(<Harness settings={withoutLoadBalancer(DEPLOY_SETTINGS_PAYMENTS)} />)
    expect(screen.getByLabelText('Health check grace period (seconds)')).toBeDisabled()
  })

  it('exec toggle warns about new tasks and missing task role', () => {
    const { unmount } = render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)
    expect(screen.getByText(execNote)).toBeInTheDocument()
    expect(screen.queryByText(/has no task role/i)).not.toBeInTheDocument()
    // With a role the requirement is only a hint.
    expect(screen.getByText(/ssmmessages:CreateControlChannel/)).toBeInTheDocument()
    unmount()

    render(<Harness settings={withoutTaskRole(DEPLOY_SETTINGS_PAYMENTS)} />)
    expect(screen.getByText(execNote)).toBeInTheDocument()
    const warning = screen.getByText(/task definition has no task role/i)
    for (const action of ['CreateControlChannel', 'CreateDataChannel', 'OpenControlChannel', 'OpenDataChannel']) {
      expect(warning).toHaveTextContent(`ssmmessages:${action}`)
    }
  })

  it('controls locked without permission', () => {
    render(<Harness settings={withCan(DEPLOY_SETTINGS_PAYMENTS, { operate: false, deploy: false })} />)
    for (const name of ['Desired count', 'Increase desired count', 'Decrease desired count', 'Force new deployment']) {
      const control = screen.getByRole(name === 'Desired count' ? 'spinbutton' : name.startsWith('Force') ? 'checkbox' : 'button', { name })
      expect(control).toBeDisabled()
      expect(control.closest('[title]')).toHaveAttribute('title', 'You need Operate on prod')
    }
    for (const name of ['ECS Exec', 'Circuit breaker', 'Roll back on failure']) {
      const control = screen.getByRole('checkbox', { name })
      expect(control).toBeDisabled()
      expect(control.closest('[title]')).toHaveAttribute('title', 'You need Deploy on prod')
    }
    expect(screen.getByLabelText('Minimum healthy percent')).toBeDisabled()
    expect(screen.getByLabelText('Maximum percent')).toBeDisabled()
    expect(screen.getByLabelText('Health check grace period (seconds)')).toBeDisabled()
  })

  it('validates desired count against the maximum', async () => {
    const user = userEvent.setup()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)
    const input = screen.getByRole('spinbutton', { name: 'Desired count' })
    await user.clear(input)
    await user.type(input, '51')
    expect(screen.getByText(/between 0 and 50/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Decrease desired count' }))
    expect(latestDraft.desiredCount).toBe(2)
  })
})
