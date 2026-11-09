// Types and hooks for the off-hours schedule endpoints. They mirror internal/api/offhours_dto.go.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './client'
import type { TagDTO } from './types'

/** Seven rows (Monday first) of 24 hourly flags; true means awake. */
export type AwakeGrid = boolean[][]

/** asleep, awake and woken follow the grid; the rest explain why a service is not on it. */
export type OffHoursState = 'asleep' | 'awake' | 'woken' | 'blocked' | 'pending' | 'not_managed' | 'conflict'

export interface OffHoursService {
  service_id: string
  service: string
  cluster: string
  account_name: string
  region: string
  tag: TagDTO
  awake_tasks: number
  current_tasks: number
  state: OffHoursState
  /** Next wake for asleep, next sleep for awake, end of the window for woken. */
  until: string | null
  /** The sentence for blocked, not_managed, conflict and needs-attention rows. */
  reason: string
  /** Dollars per month always on; null when the service has no Fargate price. */
  monthly_always_on: number | null
  /** Why the service looks like production (a warning, never a block). */
  looks_like_production: string[]
  can_operate: boolean
}

export interface OffHoursSchedule {
  id: number
  name: string
  enabled: boolean
  tag_key: string
  tag_value: string
  timezone: string
  awake: AwakeGrid
  awake_hours_per_week: number
  monthly_saving: number | null
  unpriced_services: number
  service_count: number
  asleep_count: number
  blocked_count: number
  version: number
  updated_at: string
  updated_by: string
  /** Filled by the detail endpoint; empty in the list. */
  services: OffHoursService[]
}

export interface OffHoursList {
  items: OffHoursSchedule[]
  monthly_saving_total: number | null
  prices_as_of: string
  now: string
}

export interface OffHoursScheduleInput {
  name: string
  enabled: boolean
  tag_key: string
  tag_value: string
  timezone: string
  awake: AwakeGrid
  /** Required on update. */
  version?: number
}

export interface OffHoursPreviewRequest extends OffHoursScheduleInput {
  /** The schedule being edited, so it does not overlap itself. */
  schedule_id?: number
}

export interface OffHoursOverlap {
  schedule_id: number
  schedule_name: string
  services: string[]
}

export interface OffHoursPreview {
  services: OffHoursService[]
  overlaps: OffHoursOverlap[]
  sleep_now: number
  monthly_saving: number | null
  awake_hours_per_week: number
}

export interface OffHoursTagOption {
  key: string
  value: string
  services: number
}

export interface ServiceOffHours {
  /** True once the reconciler has taken the service on; a matched service not yet taken on has enrolled false. */
  enrolled: boolean
  schedule_name: string
  timezone: string
  /** Empty when no schedule matches the service. */
  state: OffHoursState | ''
  until: string | null
  reason: string
  can_operate: boolean
}

export interface WakeResponse {
  woken_until: string
}

const SCHEDULES_KEY = ['offhours', 'schedules'] as const

export function useOffHoursSchedules() {
  return useQuery({ queryKey: SCHEDULES_KEY, queryFn: () => api.get<OffHoursList>('/api/offhours/schedules') })
}

export function useOffHoursSchedule(id: number | null) {
  return useQuery({
    queryKey: ['offhours', 'schedule', id],
    queryFn: () => api.get<OffHoursSchedule>(`/api/offhours/schedules/${id}`),
    enabled: id !== null,
  })
}

export function useOffHoursTags() {
  return useQuery({
    queryKey: ['offhours', 'tags'],
    queryFn: () => api.get<{ items: OffHoursTagOption[] }>('/api/offhours/tags'),
  })
}

/** Previews what a schedule would match and do; nothing is written. Call `mutate` as the editor changes. */
export function useOffHoursPreview() {
  return useMutation({ mutationFn: (body: OffHoursPreviewRequest) => api.post<OffHoursPreview>('/api/offhours/preview', body) })
}

export function useServiceOffHours(serviceId: string) {
  return useQuery({
    queryKey: ['service', serviceId, 'offhours'],
    queryFn: () => api.get<ServiceOffHours>(`/api/services/${serviceId}/offhours`),
  })
}

function useInvalidateOffHours() {
  const queryClient = useQueryClient()
  return () => Promise.all([queryClient.invalidateQueries({ queryKey: ['offhours'] }), queryClient.invalidateQueries({ queryKey: ['service'] })])
}

export function useCreateOffHoursSchedule() {
  const invalidate = useInvalidateOffHours()
  return useMutation({
    mutationFn: (body: OffHoursScheduleInput) => api.post<OffHoursSchedule>('/api/offhours/schedules', body),
    onSuccess: invalidate,
  })
}

export function useUpdateOffHoursSchedule(id: number) {
  const invalidate = useInvalidateOffHours()
  return useMutation({
    mutationFn: (body: OffHoursScheduleInput) => api.put<OffHoursSchedule>(`/api/offhours/schedules/${id}`, body),
    onSuccess: invalidate,
  })
}

export function useDeleteOffHoursSchedule() {
  const invalidate = useInvalidateOffHours()
  return useMutation({
    mutationFn: (id: number) => api.del(`/api/offhours/schedules/${id}`),
    onSuccess: invalidate,
  })
}

export function useWakeService(serviceId: string) {
  const invalidate = useInvalidateOffHours()
  return useMutation({
    mutationFn: () => api.post<WakeResponse>(`/api/services/${serviceId}/offhours/wake`),
    onSuccess: invalidate,
  })
}

export function useSleepService(serviceId: string) {
  const invalidate = useInvalidateOffHours()
  return useMutation({
    mutationFn: () => api.post<Record<string, never>>(`/api/services/${serviceId}/offhours/sleep`),
    onSuccess: invalidate,
  })
}
