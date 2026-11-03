import type { DeploySettingsResponse } from '../api/deploy'
import { DEPLOY_SETTINGS_PAYMENTS } from './fixtures'

/** No load balancer, no task role, exec off and no scalable target: the bare service. */
export const DEPLOY_SETTINGS_BARE: DeploySettingsResponse = {
  ...DEPLOY_SETTINGS_PAYMENTS,
  enable_execute_command: false,
  task_role_arn: '',
  has_load_balancer: false,
  health_check_grace_period_seconds: null,
  target_groups: [],
  scaling: { has_target: false, min: 0, max: 0, policies: [], scheduled_actions: [] },
}

export const withoutLoadBalancer = (settings: DeploySettingsResponse): DeploySettingsResponse => ({
  ...settings,
  has_load_balancer: false,
  health_check_grace_period_seconds: null,
  target_groups: [],
})

export const withoutTaskRole = (settings: DeploySettingsResponse): DeploySettingsResponse => ({ ...settings, task_role_arn: '' })

export const withoutScalingTarget = (settings: DeploySettingsResponse): DeploySettingsResponse => ({
  ...settings,
  scaling: { has_target: false, min: 0, max: 0, policies: [], scheduled_actions: [] },
})

export const withCan = (settings: DeploySettingsResponse, can: Partial<DeploySettingsResponse['can']>): DeploySettingsResponse => ({
  ...settings,
  can: { ...settings.can, ...can },
})
