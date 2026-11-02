import { useQueries } from '@tanstack/react-query'
import { api } from '../../api/client'
import { useNetworks, type RegionVpcsResponse } from '../../api/networks'

export interface VpcEntry {
  /** `account/region/vpc-id`; the value of the `vpc` URL parameter. */
  key: string
  accountId: string
  accountName: string
  region: string
  vpcId: string
  name: string
  cidr: string
  clusterCount: number
  tasks: number
  subnets: number
}

const LIVE_REFETCH_MS = 5_000

/**
 * Every active VPC the user can see, in account then region order. The accounts response lists the regions with active
 * VPCs; each of those regions is then read once (the same cache entries the region endpoint always used).
 */
export function useVpcList() {
  const networks = useNetworks()
  const accounts = networks.data?.items ?? []
  const regions = accounts.flatMap((account) =>
    account.regions.filter((region) => region.active_vpcs > 0).map((region) => ({ account, region: region.region })),
  )
  const results = useQueries({
    queries: regions.map(({ account, region }) => ({
      queryKey: ['networks', account.account_id, region],
      queryFn: () =>
        api.get<RegionVpcsResponse>(`/api/networks/${encodeURIComponent(account.account_id)}/${encodeURIComponent(region)}`),
      refetchInterval: LIVE_REFETCH_MS,
      refetchIntervalInBackground: false,
    })),
  })

  const vpcs: VpcEntry[] = regions.flatMap(({ account, region }, index) =>
    (results[index]?.data?.items ?? []).map((vpc) => ({
      key: `${account.account_id}/${region}/${vpc.vpc_id}`,
      accountId: account.account_id,
      accountName: account.account_name,
      region,
      vpcId: vpc.vpc_id,
      name: vpc.name,
      cidr: vpc.cidr,
      clusterCount: vpc.clusters.length,
      tasks: vpc.tasks,
      subnets: vpc.subnets,
    })),
  )
  const regionStats = accounts.flatMap((account) => account.regions)
  return {
    vpcs,
    accountCount: accounts.length,
    hiddenVpcs: regionStats.reduce((sum, region) => sum + region.hidden_vpcs, 0),
    isLoading: networks.isLoading || results.some((result) => result.isLoading),
    error: networks.error ?? results.find((result) => result.error)?.error ?? null,
  }
}
