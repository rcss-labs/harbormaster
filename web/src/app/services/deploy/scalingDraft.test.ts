import { describe, expect, it } from 'vitest'
import type { ChangeSetPolicyPut, DeployPolicyDTO, DeploySettingsResponse } from '../../../api/deploy'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { DEPLOY_SETTINGS_BARE } from '../../../test/deployVariants'
import { scalingDraftFromSettings, scalingDraftToChange, type ScalingDraft } from './scalingDraft'

const TG = DEPLOY_SETTINGS_PAYMENTS.target_groups[0]
const step: DeployPolicyDTO = {
  name: 'step-out',
  type: 'StepScaling',
  metric: 'other',
  metric_raw: '',
  target_value: 0,
  scale_in_cooldown: 0,
  scale_out_cooldown: 0,
  disable_scale_in: false,
  resource_label: '',
  editable: false,
}
const withStep: DeploySettingsResponse = {
  ...DEPLOY_SETTINGS_PAYMENTS,
  scaling: { ...DEPLOY_SETTINGS_PAYMENTS.scaling, policies: [...DEPLOY_SETTINGS_PAYMENTS.scaling.policies, step] },
}

const cpuPut: ChangeSetPolicyPut = {
  name: 'cpu-target-60',
  metric: 'cpu',
  target_value: 60,
  scale_in_cooldown: 300,
  scale_out_cooldown: 60,
  disable_scale_in: false,
  target_group_arn: '',
}

interface Case {
  name: string
  before: DeploySettingsResponse
  edit: (draft: ScalingDraft) => ScalingDraft
  want: ReturnType<typeof scalingDraftToChange>
}

const cases: Case[] = [
  { name: 'nothing changed', before: DEPLOY_SETTINGS_PAYMENTS, edit: (d) => d, want: null },
  { name: 'no target and none added', before: DEPLOY_SETTINGS_BARE, edit: (d) => d, want: null },
  { name: 'target range changed', before: DEPLOY_SETTINGS_PAYMENTS, edit: (d) => ({ ...d, max: 12 }), want: { target: { min: 2, max: 12 } } },
  {
    name: 'target added with a policy and a scheduled action',
    before: DEPLOY_SETTINGS_BARE,
    edit: (d) => ({
      ...d,
      hasTarget: true,
      min: 1,
      max: 4,
      policies: [{ ...DEPLOY_SETTINGS_PAYMENTS.scaling.policies[0], target_group_arn: '' }],
      scheduledActions: [{ name: 'night', schedule: 'rate(1 day)', timezone: 'UTC', min_capacity: 1, max_capacity: null }],
    }),
    want: {
      target: { min: 1, max: 4 },
      put_policies: [cpuPut],
      put_scheduled_actions: [{ name: 'night', schedule: 'rate(1 day)', timezone: 'UTC', min_capacity: 1, max_capacity: null }],
    },
  },
  {
    name: 'policy edited',
    before: DEPLOY_SETTINGS_PAYMENTS,
    edit: (d) => ({ ...d, policies: d.policies.map((p) => ({ ...p, target_value: 70 })) }),
    want: { put_policies: [{ ...cpuPut, target_value: 70 }] },
  },
  {
    name: 'policy removed',
    before: DEPLOY_SETTINGS_PAYMENTS,
    edit: (d) => ({ ...d, policies: [] }),
    want: { delete_policies: ['cpu-target-60'] },
  },
  {
    name: 'ALB policy added carries its target group',
    before: DEPLOY_SETTINGS_PAYMENTS,
    edit: (d) => ({
      ...d,
      policies: [
        ...d.policies,
        { ...step, name: 'alb-req', type: 'TargetTrackingScaling', metric: 'alb', target_value: 800, editable: true, target_group_arn: TG.arn },
      ],
    }),
    want: {
      put_policies: [{ name: 'alb-req', metric: 'alb', target_value: 800, scale_in_cooldown: 0, scale_out_cooldown: 0, disable_scale_in: false, target_group_arn: TG.arn }],
    },
  },
  {
    name: 'read-only step policy is never put, only deleted',
    before: withStep,
    edit: (d) => ({ ...d, policies: d.policies.filter((p) => p.name !== 'step-out') }),
    want: { delete_policies: ['step-out'] },
  },
  {
    name: 'scheduled action edited and another removed',
    before: {
      ...DEPLOY_SETTINGS_PAYMENTS,
      scaling: {
        ...DEPLOY_SETTINGS_PAYMENTS.scaling,
        scheduled_actions: [
          ...DEPLOY_SETTINGS_PAYMENTS.scaling.scheduled_actions,
          { name: 'old', schedule: 'rate(1 hour)', timezone: 'UTC', min_capacity: null, max_capacity: 5 },
        ],
      },
    },
    edit: (d) => ({
      ...d,
      scheduledActions: d.scheduledActions.filter((a) => a.name !== 'old').map((a) => ({ ...a, max_capacity: 6 })),
    }),
    want: {
      put_scheduled_actions: [{ name: 'business-hours', schedule: 'cron(0 7 * * ? *)', timezone: 'Asia/Jerusalem', min_capacity: 3, max_capacity: 6 }],
      delete_scheduled_actions: ['old'],
    },
  },
  {
    name: 'target removed deletes every policy and scheduled action',
    before: withStep,
    edit: (d) => ({ ...d, hasTarget: false, policies: [], scheduledActions: [] }),
    want: {
      remove_target: true,
      delete_policies: ['cpu-target-60', 'step-out'],
      delete_scheduled_actions: ['business-hours'],
    },
  },
  {
    name: 'empty min is not emitted',
    before: DEPLOY_SETTINGS_PAYMENTS,
    edit: (d) => ({ ...d, min: null }),
    want: null,
  },
]

describe('scalingDraft', () => {
  it.each(cases)('emits put/delete sets from before and after: $name', ({ before, edit, want }) => {
    const draft = edit(scalingDraftFromSettings(before))
    expect(scalingDraftToChange(before, draft)).toEqual(want)
  })
})
