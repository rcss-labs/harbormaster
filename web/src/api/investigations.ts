// Types and hooks for the investigation read API. They mirror the contract of internal/api/investigations_dto.go.
// Every hook is off unless the current-user DTO says the agent is enabled, so a server without it sees no request.
import { useQuery, type UseQueryResult } from '@tanstack/react-query'
import { useState } from 'react'
import { useAuth } from '../app/auth/AuthProvider'
import { api } from './client'
import { LIVE_REFETCH_MS } from './polling'

export interface InvestigationSubjectDTO {
  kind: string
  service_id: string
  service: string
  cluster: string
  cluster_id: string
  account_id: string
  region: string
}

export interface InvestigationDTO {
  id: number
  state: string
  reason: string
  subject: InvestigationSubjectDTO
  alert: { kind: string; title: string }
  requested_at: string
  started_at: string | null
  finished_at: string | null
  summary: string
  summary_replaced: boolean
}

export interface InvestigationMemberDTO {
  service_id: string
  service: string
  cluster: string
}

export interface InvestigationEvidenceDTO {
  ordinal: number
  tool: string
  /** `view` or `logs`: the permission the excerpt needed. */
  action: string
  service_id: string
  service: string
  note: string
  excerpt: string
}

export interface InvestigationDetailDTO extends InvestigationDTO {
  members: InvestigationMemberDTO[]
  evidence: InvestigationEvidenceDTO[]
  /** How many items the reader may not see; never named. */
  evidence_hidden: number
}

export const serviceInvestigationsKey = (serviceId: string) => ['service', serviceId, 'investigations'] as const
export const investigationKey = (id: number) => ['investigation', id] as const
export const incidentInvestigationKey = (incidentId: string) => ['investigation', 'incident', incidentId] as const

const LIST_REFETCH_MS = 15_000
const POLL_LIMIT_MS = 30 * 60_000

/** Only these two states are still moving; any other, an unknown one included, is final for polling. */
const isMoving = (state: string | undefined) => state === 'requested' || state === 'running'

/** The time this hook started, so polling gives up 30 minutes after the view opened. Not read during render. */
const useStartedAt = () => useState(() => Date.now())[0]

function useAgentEnabled(): boolean {
  return Boolean(useAuth().me?.agent_enabled)
}

export function useServiceInvestigations(serviceId: string): UseQueryResult<{ items: InvestigationDTO[] }> {
  const enabled = useAgentEnabled()
  const startedAt = useStartedAt()
  return useQuery({
    queryKey: serviceInvestigationsKey(serviceId),
    queryFn: () => api.get<{ items: InvestigationDTO[] }>(`/api/services/${encodeURIComponent(serviceId)}/investigations`),
    enabled,
    refetchInterval: (query) =>
      Date.now() - startedAt < POLL_LIMIT_MS && query.state.data?.items.some((item) => isMoving(item.state)) ? LIST_REFETCH_MS : false,
    refetchIntervalInBackground: false,
  })
}

export function useInvestigation(id: number | null): UseQueryResult<InvestigationDetailDTO> {
  const enabled = useAgentEnabled()
  const startedAt = useStartedAt()
  return useQuery({
    queryKey: investigationKey(id ?? 0),
    queryFn: () => api.get<InvestigationDetailDTO>(`/api/investigations/${encodeURIComponent(String(id))}`),
    enabled: enabled && id !== null,
    refetchInterval: (query) => (Date.now() - startedAt < POLL_LIMIT_MS && isMoving(query.state.data?.state) ? LIVE_REFETCH_MS : false),
    refetchIntervalInBackground: false,
  })
}

export function useIncidentInvestigation(incidentId: string, wanted = true): UseQueryResult<InvestigationDetailDTO> {
  const enabled = useAgentEnabled()
  return useQuery({
    queryKey: incidentInvestigationKey(incidentId),
    queryFn: () => api.get<InvestigationDetailDTO>(`/api/investigations/by-incident/${encodeURIComponent(incidentId)}`),
    enabled: enabled && wanted,
  })
}
