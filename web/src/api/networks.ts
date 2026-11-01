// TanStack hooks and wire types for the networks (VPC) endpoints. Networks pages refresh every 5 s like the other live
// data, and not while the tab is in the background. Shapes mirror internal/api/networks.go and internal/network/tree.go.
import { useQuery } from '@tanstack/react-query'
import { useFilters } from '../app/shell/useFilters'
import { api } from './client'
import { queryString, type Filters } from './hooks'
import type { Status } from './types'

const LIVE_REFETCH_MS = 5_000
const livePolling = { refetchInterval: LIVE_REFETCH_MS, refetchIntervalInBackground: false } as const

export interface NetworkRegion {
  region: string
  active_vpcs: number
  hidden_vpcs: number
  clusters: number
  tasks: number
  unplaced_tasks: number
}

export interface NetworkAccount {
  account_id: string
  account_name: string
  regions: NetworkRegion[]
}

export interface NetworksResponse {
  items: NetworkAccount[]
  polled_at: string
}

export interface NetworkVpcCard {
  vpc_id: string
  name: string
  cidr: string
  subnets: number
  clusters: string[]
  tasks: number
}

export interface RegionVpcsResponse {
  items: NetworkVpcCard[]
  polled_at: string
}

export interface VpcRefHeader {
  vpc_id: string
  name: string
  cidr: string
  account_id: string
  account_name: string
  region: string
}

export interface TreeEdge {
  kind: string
  id: string
  subnet_id?: string
  detail?: string
}

export interface TreeSummary {
  subnets: number
  zones: number
  running_tasks: number
  unplaced_tasks: number
  collapsed_by_default: boolean
}

interface TreeNodeBase {
  key: string
  label: string
  tasks: number
  children?: TreeNode[]
}

export type TreeNode =
  | (TreeNodeBase & { kind: 'vpc'; vpc: { vpc_id: string; cidr: string; zones: number } })
  | (TreeNodeBase & { kind: 'az'; az: { subnets: number } })
  | (TreeNodeBase & { kind: 'nosubnet' })
  | (TreeNodeBase & { kind: 'subnet'; subnet: { subnet_id: string; cidr: string; public: boolean; ips_used: number; ips_total: number } })
  | (TreeNodeBase & { kind: 'cluster'; cluster: { cluster_id: string; tag: { name: string; color: string }; services: number } })
  | (TreeNodeBase & { kind: 'service'; service: { service_id: string; status: Status; revision: number } })
  | (TreeNodeBase & { kind: 'standalone' })
  | (TreeNodeBase & { kind: 'task'; task: { task_id: string; service_id?: string; private_ip: string; group?: string; unhealthy: boolean } })

export interface VpcTreeResponse {
  vpc: VpcRefHeader
  edges: TreeEdge[]
  summary: TreeSummary
  root: TreeNode
  polled_at: string
}

/** Where a tree node links to, or undefined for grouping nodes (VPC, AZ, subnet) that have no page of their own. */
export function nodeHref(node: TreeNode): string | undefined {
  switch (node.kind) {
    case 'cluster':
      return `/clusters/${node.cluster.cluster_id}`
    case 'service':
      return `/services/${node.service.service_id}/overview`
    case 'task':
      return node.task.service_id ? `/services/${node.task.service_id}/tasks` : '/tasks'
    default:
      return undefined
  }
}

export function useNetworks() {
  const { filters } = useFilters()
  return useQuery({
    queryKey: ['networks', filters],
    queryFn: () => api.get<NetworksResponse>(`/api/networks${queryString({ ...filters })}`),
    ...livePolling,
  })
}

export function useNetworkRegion(account: string, region: string) {
  return useQuery({
    queryKey: ['networks', account, region],
    queryFn: () =>
      api.get<RegionVpcsResponse>(`/api/networks/${encodeURIComponent(account)}/${encodeURIComponent(region)}`),
    ...livePolling,
  })
}

export function useVpcTree(account: string, region: string, vpcId: string) {
  const filters: Filters = { account, region }
  return useQuery({
    queryKey: ['vpcTree', account, region, vpcId],
    queryFn: () => api.get<VpcTreeResponse>(`/api/vpcs/${encodeURIComponent(vpcId)}/tree${queryString({ ...filters })}`),
    ...livePolling,
  })
}
