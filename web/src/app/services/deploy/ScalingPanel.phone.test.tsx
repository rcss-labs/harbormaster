import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { withCan } from '../../../test/deployVariants'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { installPhoneMedia } from '../../../test/phone'
import { ScalingPanel } from './ScalingPanel'
import { scalingDraftFromSettings, type ScalingDraft } from './scalingDraft'

let latestDraft: ScalingDraft

function Harness({ settings }: { settings: DeploySettingsResponse }) {
  const [draft, setDraft] = useState(() => (latestDraft = scalingDraftFromSettings(settings)))
  return (
    <ScalingPanel
      settings={settings}
      draft={draft}
      onChange={(next) => {
        latestDraft = next
        setDraft(next)
      }}
      desiredCount={settings.desired_count}
      tagName="prod"
      onRetry={() => {}}
    />
  )
}

let restorePhone: () => void
beforeEach(() => {
  restorePhone = installPhoneMedia(true)
})
afterEach(() => restorePhone())

describe('ScalingPanel on a phone', () => {
  it('a policy row opens a sheet with Edit and Remove; Edit opens the editor in a tall sheet', async () => {
    const user = userEvent.setup()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)
    await user.click(screen.getByRole('button', { name: 'Actions for policy cpu-target-60' }))
    const menu = screen.getByRole('menu', { name: 'Actions for policy cpu-target-60' })
    await user.click(within(menu).getByRole('menuitem', { name: 'Edit' }))

    const sheet = screen.getByRole('dialog', { name: 'Edit cpu-target-60' })
    expect(within(sheet).getByRole('button', { name: 'Save policy' })).toBeInTheDocument()
    await user.click(within(sheet).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog', { name: 'Edit cpu-target-60' })).not.toBeInTheDocument()
  })

  it('Remove in the sheet takes the policy out of the draft', async () => {
    const user = userEvent.setup()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)
    await user.click(screen.getByRole('button', { name: 'Actions for policy cpu-target-60' }))
    await user.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Remove' }))
    expect(latestDraft.policies.some((policy) => policy.name === 'cpu-target-60')).toBe(false)
  })

  it('without Operate the sheet rows are disabled and say why inside the row', async () => {
    const user = userEvent.setup()
    render(<Harness settings={withCan(DEPLOY_SETTINGS_PAYMENTS, { operate: false })} />)
    await user.click(screen.getByRole('button', { name: 'Actions for policy cpu-target-60' }))
    const menu = within(screen.getByRole('menu'))
    expect(menu.getByRole('menuitem', { name: /Edit/ })).toBeDisabled()
    expect(menu.getAllByText('You need Operate on prod')).toHaveLength(2)
    expect(menu.getByRole('menuitem', { name: /Remove/ })).toBeDisabled()
  })

  it('a scheduled action row has the same sheet', async () => {
    const user = userEvent.setup()
    render(<Harness settings={DEPLOY_SETTINGS_PAYMENTS} />)
    await user.click(screen.getByRole('button', { name: 'Actions for scheduled action business-hours' }))
    expect(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Edit' })).toBeEnabled()
  })
})
