import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { CapacityFields } from './CapacityFields'
import { capacityDraftFromSettings, spotCapacityDraft } from './serviceDraft'

const withCapacity = (capacity: Partial<DeploySettingsResponse['capacity']>): DeploySettingsResponse => ({
  ...DEPLOY_SETTINGS_PAYMENTS,
  capacity: { ...DEPLOY_SETTINGS_PAYMENTS.capacity, ...capacity },
})

function show(settings: DeploySettingsResponse, draft = capacityDraftFromSettings(settings)) {
  const onChange = vi.fn()
  render(<CapacityFields settings={settings} draft={draft} onChange={onChange} tagName="Prod" clusterName="core-services" />)
  return onChange
}

describe('CapacityFields', () => {
  it('shows a launch type service as one on-demand provider', () => {
    show(DEPLOY_SETTINGS_PAYMENTS)
    expect(screen.getByLabelText('On-demand tasks kept (base)')).toHaveValue(0)
    expect(screen.getByLabelText('On-demand weight')).toHaveValue(1)
    expect(screen.getByLabelText('Spot weight')).toHaveValue(0)
  })

  it('reports an edited number', async () => {
    const user = userEvent.setup()
    const onChange = show(DEPLOY_SETTINGS_PAYMENTS)
    await user.type(screen.getByLabelText('Spot weight'), '2')
    expect(onChange).toHaveBeenLastCalledWith({ onDemandBase: 0, onDemandWeight: 1, spotWeight: 2 })
  })

  it('disables the fields with the Deploy lock', () => {
    show({ ...DEPLOY_SETTINGS_PAYMENTS, can: { ...DEPLOY_SETTINGS_PAYMENTS.can, deploy: false } })
    for (const name of ['On-demand tasks kept (base)', 'On-demand weight', 'Spot weight']) {
      expect(screen.getByLabelText(name)).toBeDisabled()
      expect(screen.getByLabelText(name).closest('label')).toHaveAttribute('title', 'You need Deploy on Prod')
    }
  })

  it('locks only Spot when the cluster has no FARGATE_SPOT', () => {
    show(withCapacity({ cluster_providers: ['FARGATE'] }))
    expect(screen.getByLabelText('Spot weight')).toBeDisabled()
    expect(screen.getByLabelText('On-demand weight')).toBeEnabled()
  })

  it('lets a service that already uses Spot lower it even if the cluster lost the provider', () => {
    const settings = withCapacity({
      launch_type: '',
      cluster_providers: ['FARGATE'],
      strategy: [{ provider: 'FARGATE_SPOT', base: 0, weight: 1 }],
    })
    show(settings)
    expect(screen.getByLabelText('Spot weight')).toBeEnabled()
  })

  it('warns that tasks are replaced whenever the capacity changed, Spot or not', () => {
    const live = capacityDraftFromSettings(DEPLOY_SETTINGS_PAYMENTS)
    show(DEPLOY_SETTINGS_PAYMENTS, { ...live, onDemandBase: 1 })
    expect(screen.getByRole('status')).toHaveTextContent('Applying this starts a new deployment that replaces every task')
    expect(screen.getByRole('status')).not.toHaveTextContent('Spot tasks can be stopped')
  })

  it('shows no warning for the live capacity', () => {
    show(DEPLOY_SETTINGS_PAYMENTS)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('warns once the draft puts tasks on Spot, and not for the live strategy', () => {
    show(DEPLOY_SETTINGS_PAYMENTS, spotCapacityDraft(1))
    expect(screen.getByRole('status')).toHaveTextContent('ECS does not fall back to on-demand')
  })
})
