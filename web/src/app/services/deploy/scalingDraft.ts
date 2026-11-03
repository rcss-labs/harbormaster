import type {
  ChangeSetPolicyPut,
  ChangeSetRequest,
  DeployPolicyDTO,
  DeployScheduledActionDTO,
  DeploySettingsResponse,
} from '../../../api/deploy'

/** A policy as edited: the server DTO plus the target group an ALB policy is bound to. */
export interface PolicyDraft extends DeployPolicyDTO {
  target_group_arn: string
}

export type ScheduledActionDraft = DeployScheduledActionDTO

export interface ScalingDraft {
  hasTarget: boolean
  /** Null while the field is empty; such a target is reported by `scalingDraftErrors` and not sent. */
  min: number | null
  max: number | null
  policies: PolicyDraft[]
  scheduledActions: ScheduledActionDraft[]
}

export type ScalingChange = NonNullable<ChangeSetRequest['scaling']>

export function scalingDraftFromSettings(settings: DeploySettingsResponse): ScalingDraft {
  const { scaling, target_groups: targetGroups } = settings
  return {
    hasTarget: scaling.has_target,
    min: scaling.has_target ? scaling.min : null,
    max: scaling.has_target ? scaling.max : null,
    policies: scaling.policies.map((policy) => ({
      ...policy,
      target_group_arn: targetGroups.find((group) => policy.resource_label !== '' && group.resource_label === policy.resource_label)?.arn ?? '',
    })),
    scheduledActions: scaling.scheduled_actions.map((action) => ({ ...action })),
  }
}

const validRange = (min: number | null, max: number | null, ceiling: number): min is number =>
  min !== null && max !== null && Number.isInteger(min) && Number.isInteger(max) && min >= 0 && min <= max && max <= ceiling

/** Problems that keep the scaling section from being sent. */
export function scalingDraftErrors(settings: DeploySettingsResponse, draft: ScalingDraft): string[] {
  if (!draft.hasTarget) return []
  return validRange(draft.min, draft.max, settings.max_desired)
    ? []
    : [`Auto scaling minimum and maximum must be whole numbers with minimum at most maximum, up to ${settings.max_desired}.`]
}

function toPut(policy: PolicyDraft): ChangeSetPolicyPut | null {
  if (!policy.editable || policy.metric === 'other') return null
  return {
    name: policy.name,
    metric: policy.metric,
    target_value: policy.target_value,
    scale_in_cooldown: policy.scale_in_cooldown,
    scale_out_cooldown: policy.scale_out_cooldown,
    disable_scale_in: policy.disable_scale_in,
    target_group_arn: policy.metric === 'alb' ? policy.target_group_arn : '',
  }
}

const sameJSON = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

/**
 * The `scaling` section of a change set: what must be written so the live configuration (`before`) becomes the draft (`after`).
 * Items are matched by name. Null when nothing differs.
 */
export function scalingDraftToChange(before: DeploySettingsResponse, after: ScalingDraft): ScalingChange | null {
  const change: ScalingChange = {}
  const beforeDraft = scalingDraftFromSettings(before)

  if (!after.hasTarget) {
    if (!before.scaling.has_target) return null
    change.remove_target = true
    // Deregistering drops them server side; listing them keeps the review explicit.
    if (beforeDraft.policies.length > 0) change.delete_policies = beforeDraft.policies.map((policy) => policy.name)
    if (beforeDraft.scheduledActions.length > 0) change.delete_scheduled_actions = beforeDraft.scheduledActions.map((action) => action.name)
    return change
  }

  if (validRange(after.min, after.max, before.max_desired) && (!before.scaling.has_target || after.min !== before.scaling.min || after.max !== before.scaling.max)) {
    change.target = { min: after.min, max: after.max as number }
  }

  const beforePolicies = new Map(beforeDraft.policies.map((policy) => [policy.name, policy]))
  const putPolicies: ChangeSetPolicyPut[] = []
  for (const policy of after.policies) {
    const put = toPut(policy)
    const previous = beforePolicies.get(policy.name)
    if (put && (!previous || !sameJSON(toPut(previous), put))) putPolicies.push(put)
  }
  const afterPolicyNames = new Set(after.policies.map((policy) => policy.name))
  const deletePolicies = beforeDraft.policies.filter((policy) => !afterPolicyNames.has(policy.name)).map((policy) => policy.name)

  const beforeActions = new Map(beforeDraft.scheduledActions.map((action) => [action.name, action]))
  const putActions = after.scheduledActions.filter((action) => {
    const previous = beforeActions.get(action.name)
    return !previous || !sameJSON(previous, action)
  })
  const afterActionNames = new Set(after.scheduledActions.map((action) => action.name))
  const deleteActions = beforeDraft.scheduledActions.filter((action) => !afterActionNames.has(action.name)).map((action) => action.name)

  if (putPolicies.length > 0) change.put_policies = putPolicies
  if (deletePolicies.length > 0) change.delete_policies = deletePolicies
  if (putActions.length > 0) change.put_scheduled_actions = putActions.map((action) => ({ ...action }))
  if (deleteActions.length > 0) change.delete_scheduled_actions = deleteActions
  return Object.keys(change).length === 0 ? null : change
}
