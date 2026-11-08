import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { withCan, withoutScalingTarget } from '../../../test/deployVariants'
import { ScalingPanel } from './ScalingPanel'
import { scalingDraftFromSettings, scalingDraftToChange, type ScalingDraft } from './scalingDraft'

let latestDraft: ScalingDraft

function Harness({ settings, desiredCount = settings.desired_count }: { settings: DeploySettingsResponse; desiredCount?: number | null }) {
  const [draft, setDraft] = useState(() => (latestDraft = scalingDraftFromSettings(settings)))
  return <ScalingPanel settings={settings} draft={draft} onChange={(next) => {
        latestDraft = next
        setDraft(next)
      }} desiredCount={desiredCount} tagName="prod" onRetry={() => {}} />
}

describe('ScalingPanel', () => {
  it('adding a target enables policy editing', async () => {
    const user = userEvent.setup()
    const settings = withoutScalingTarget(DEPLOY_SETTINGS_PAYMENTS)
    render(<Harness settings={settings} />)

    expect(screen.getByText('No auto scaling')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add policy' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add scheduled action' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Add auto scaling target' }))
    expect(screen.getByRole('button', { name: 'Add policy' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Add scheduled action' })).toBeEnabled()
    expect(latestDraft.hasTarget).toBe(true)
    expect(scalingDraftToChange(settings, latestDraft)?.target).toEqual({ min: latestDraft.min, max: latestDraft.max })
  })

  it('ALB policy requires a target group', async () => {
    const user = userEvent.setup()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)

    await user.click(screen.getByRole('button', { name: 'Add policy' }))
    await user.type(screen.getByLabelText('Policy name'), 'alb-req')
    await user.selectOptions(screen.getByLabelText('Metric'), 'alb')
    await user.clear(screen.getByLabelText('Target value'))
    await user.type(screen.getByLabelText('Target value'), '800')
    const save = screen.getByRole('button', { name: 'Save policy' })
    expect(save).toBeDisabled()
    expect(screen.getByText('Choose the target group that receives the requests.')).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('Target group'), 'payments-api')
    expect(save).toBeEnabled()
    await user.click(save)

    expect(latestDraft.policies.map((p) => p.name)).toContain('alb-req')
    const put = scalingDraftToChange(DEPLOY_SETTINGS_PAYMENTS, latestDraft)?.put_policies
    expect(put).toEqual([expect.objectContaining({ name: 'alb-req', metric: 'alb', target_value: 800, target_group_arn: DEPLOY_SETTINGS_PAYMENTS.target_groups[0].arn })])
  })

  it('removing the target marks policies and actions for removal', async () => {
    const user = userEvent.setup()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)

    await user.click(screen.getByRole('button', { name: 'Remove target' }))
    expect(screen.getByText(/also removes 1 policy and 1 scheduled action/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add policy' })).not.toBeInTheDocument()
    expect(scalingDraftToChange(DEPLOY_SETTINGS_PAYMENTS, latestDraft)).toEqual({
      remove_target: true,
      delete_policies: ['cpu-target-60'],
      delete_scheduled_actions: ['business-hours'],
    })

    await user.click(screen.getByRole('button', { name: 'Keep target' }))
    expect(scalingDraftToChange(DEPLOY_SETTINGS_PAYMENTS, latestDraft)).toBeNull()
  })

  it('lists policies with their metric labels and read-only rows only offer Remove', async () => {
    const settings: DeploySettingsResponse = {
      ...DEPLOY_SETTINGS_PAYMENTS,
      scaling: {
        ...DEPLOY_SETTINGS_PAYMENTS.scaling,
        policies: [
          ...DEPLOY_SETTINGS_PAYMENTS.scaling.policies,
          { ...DEPLOY_SETTINGS_PAYMENTS.scaling.policies[0], name: 'queue-depth', metric: 'other', metric_raw: 'QueueDepth', editable: false },
        ],
      },
    }
    render(<Harness settings={settings} />)
    const table = within(screen.getByRole('table', { name: 'Scaling policies' }))
    expect(table.getByText('CPU %')).toBeInTheDocument()
    const other = table.getByText('queue-depth').closest('[role="row"]') as HTMLElement
    expect(within(other).getByRole('button', { name: 'Remove queue-depth' })).toBeInTheDocument()
    expect(within(other).queryByRole('button', { name: 'Edit queue-depth' })).not.toBeInTheDocument()
    const cpu = table.getByText('cpu-target-60').closest('[role="row"]') as HTMLElement
    expect(within(cpu).getByRole('button', { name: 'Edit cpu-target-60' })).toBeInTheDocument()
  })

  it('warns when auto scaling would move the desired count', () => {
    const { unmount } = render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} desiredCount={10} />)
    expect(screen.getByText('Auto scaling would move 10 back into 2–8.')).toBeInTheDocument()
    unmount()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} desiredCount={5} />)
    expect(screen.queryByText(/would move/)).not.toBeInTheDocument()
  })

  it('locks scaling edits without Operate', () => {
    render(<Harness settings={withCan(DEPLOY_SETTINGS_PAYMENTS, { operate: false })} />)
    const remove = screen.getByRole('button', { name: 'Remove target' })
    expect(remove).toBeDisabled()
    expect(remove.closest('[title]')).toHaveAttribute('title', 'You need Operate on prod')
    expect(screen.getByRole('button', { name: 'Add policy' })).toBeDisabled()
    expect(screen.getByLabelText('Minimum')).toBeDisabled()
  })
})
