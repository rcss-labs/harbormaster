// Lane-local stand-in for plan 02's web/src/api/change.ts hooks (same keys and endpoint). At merge, delete this file and import from '../../../api/change'.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../../api/client'

export interface DesiredCountInfo {
  desired: number
  running: number
  max: number
  auto_scaling: { min: number; max: number } | null
}

export interface SetDesiredCountResult {
  desired: number
  previous: number
  changed: boolean
  warning: string | null
}

export function useDesiredCountInfo(serviceId: string) {
  return useQuery({
    queryKey: ['service', serviceId, 'desired-count'],
    queryFn: () => api.get<DesiredCountInfo>(`/api/services/${serviceId}/desired-count`),
  })
}

export function useSetDesiredCount(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (desired: number) => api.post<SetDesiredCountResult>(`/api/services/${serviceId}/desired-count`, { desired }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['service', serviceId] }),
  })
}
