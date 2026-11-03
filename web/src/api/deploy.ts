// Deploy tab: live service settings, a registered revision's definition, and the change-set write.
// Types mirror docs/deploy_tab/PLAN.md section 2 field for field; the Go DTOs arrive in T8.
//
// The image-tags response type lives in types.ts; useContainerImageTags below is the picker's per-container, searchable read.

import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './client'
import { refetchWhileSettling } from './change'
import type { ControllerRefDTO, ImageTagsResponse } from './types'

export interface ChangeCanDTO {
  edit: boolean
  deploy: boolean
  operate: boolean
  admin: boolean
}

export interface DeployPolicyDTO {
  name: string
  type: 'TargetTrackingScaling' | 'StepScaling'
  metric: 'cpu' | 'memory' | 'alb' | 'other'
  metric_raw: string
  target_value: number
  scale_in_cooldown: number
  scale_out_cooldown: number
  disable_scale_in: boolean
  resource_label: string
  editable: boolean
}

export interface DeployScheduledActionDTO {
  name: string
  schedule: string
  timezone: string
  min_capacity: number | null
  max_capacity: number | null
}

export interface DeploymentConfigurationDTO {
  circuit_breaker: { enable: boolean; rollback: boolean }
  minimum_healthy_percent: number
  maximum_percent: number
}

export interface DeploySettingsResponse {
  /** sha256 of the canonical live settings; sent back as `base_version` on the change set. */
  version: string
  deployed_revision: number
  family: string
  controller: ControllerRefDTO
  desired_count: number
  running_count: number
  max_desired: number
  enable_execute_command: boolean
  /** Empty when the task has no role: the UI warns that Exec needs one. */
  task_role_arn: string
  deployment_configuration: DeploymentConfigurationDTO
  health_check_grace_period_seconds: number | null
  has_load_balancer: boolean
  target_groups: { arn: string; name: string; resource_label: string }[]
  scaling: {
    has_target: boolean
    min: number
    max: number
    policies: DeployPolicyDTO[]
    scheduled_actions: DeployScheduledActionDTO[]
  }
  can: ChangeCanDTO
}

export interface RevisionDefinitionResponse {
  revision: number
  arn: string
  definition: Record<string, unknown>
  registered_at: string
  registered_by: string
}

export interface ChangeSetPolicyPut {
  name: string
  metric: 'cpu' | 'memory' | 'alb'
  target_value: number
  scale_in_cooldown: number
  scale_out_cooldown: number
  disable_scale_in: boolean
  target_group_arn: string
}

export interface ChangeSetScheduledActionPut {
  name: string
  schedule: string
  timezone: string
  min_capacity: number | null
  max_capacity: number | null
}

export interface ChangeSetRequest {
  base_revision: number
  base_version: string
  /** At most 500 characters. */
  reason: string
  /** A new definition to register, or (on retry) the revision the first run already registered. */
  task_definition?: { definition: Record<string, unknown> } | { revision: number }
  service?: {
    desired_count?: number
    enable_execute_command?: boolean
    deployment_configuration?: DeploymentConfigurationDTO
    health_check_grace_period_seconds?: number
    force_new_deployment?: boolean
  }
  scaling?: {
    target?: { min: number; max: number }
    remove_target?: boolean
    put_policies?: ChangeSetPolicyPut[]
    delete_policies?: string[]
    put_scheduled_actions?: ChangeSetScheduledActionPut[]
    delete_scheduled_actions?: string[]
  }
}

export interface ChangeSetStep {
  name: 'register' | 'service' | 'scaling'
  status: 'done' | 'failed' | 'skipped'
  message: string
}

export interface ChangeSetResponse {
  status: 'applied' | 'partial' | 'registered_only'
  steps: ChangeSetStep[]
  /** Settings version after the run (partial or applied); a retry sends it as `base_version`. "" if unavailable. */
  settings_version: string
  changeset_id: string
  registered_revision: number | null
  deployment_id: string
  from_revision: number | null
  to_revision: number | null
  kind: string
  warning: string | null
}

export function useDeploySettings(serviceId: string) {
  return useQuery({
    queryKey: ['service', serviceId, 'deploy-settings'],
    queryFn: () => api.get<DeploySettingsResponse>(`/api/services/${serviceId}/deploy-settings`),
    // Fetched on tab open and after a write; no polling.
    staleTime: 30_000,
  })
}

/** The definition of one registered revision; pass null to stay idle. */
export function useRevisionDefinition(serviceId: string, revision: number | null) {
  return useQuery({
    queryKey: ['service', serviceId, 'revision-definition', revision],
    queryFn: () => api.get<RevisionDefinitionResponse>(`/api/services/${serviceId}/revisions/${revision}`),
    enabled: revision !== null,
  })
}

export function useChangeSet(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: ChangeSetRequest) => api.post<ChangeSetResponse>(`/api/services/${serviceId}/changeset`, body),
    // The stored service and scaling lag the write by a poll, so refetch them for a while.
    onSuccess: () =>
      refetchWhileSettling(async () => {
        void queryClient.invalidateQueries({ queryKey: ['operations', serviceId] })
        void queryClient.invalidateQueries({ queryKey: ['service', serviceId, 'deploy-settings'] })
        void queryClient.invalidateQueries({ queryKey: ['service', serviceId, 'scaling'] })
        await queryClient.invalidateQueries({ queryKey: ['service', serviceId] })
      }),
  })
}

/** Tags shown when the search box is empty: the newest ones. */
export const IMAGE_TAG_PAGE = 10
/** Tags listed while searching; `matched` in the response still counts all of them. */
export const IMAGE_TAG_SEARCH_PAGE = 30

/**
 * Image tags of one container, newest first; `q` searches all tags of the repository on the server.
 * The previous result stays on screen while the next search loads, so the list does not flash empty.
 */
export function useContainerImageTags(serviceId: string, container: string, q: string) {
  const search = q.trim()
  return useQuery({
    queryKey: ['service', serviceId, 'image-tags', container, search],
    queryFn: () => {
      const params = new URLSearchParams({ container, limit: String(search ? IMAGE_TAG_SEARCH_PAGE : IMAGE_TAG_PAGE) })
      if (search) params.set('q', search)
      return api.get<ImageTagsResponse>(`/api/services/${serviceId}/image-tags?${params.toString()}`)
    },
    placeholderData: keepPreviousData,
    staleTime: 60_000,
    enabled: container !== '',
  })
}
