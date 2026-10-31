import type { QueryClient } from '@tanstack/react-query'

/** The 17 commercial regions offered when connecting or editing an account (Ruling R21), in display order. */
export const COMMERCIAL_REGIONS = [
  'us-east-1',
  'us-east-2',
  'us-west-1',
  'us-west-2',
  'ca-central-1',
  'eu-west-1',
  'eu-west-2',
  'eu-west-3',
  'eu-central-1',
  'eu-north-1',
  'ap-south-1',
  'ap-southeast-1',
  'ap-southeast-2',
  'ap-northeast-1',
  'ap-northeast-2',
  'ap-northeast-3',
  'sa-east-1',
]

/** Regions checked by default in the connect dialog. */
export const DEFAULT_REGIONS = ['us-east-1', 'eu-west-1', 'eu-central-1']

/** Queries that an account change can affect: the account list, cluster and overview data, and the signed-in user's accounts. */
const AFFECTED_QUERY_KEYS = ['accounts', 'clusters', 'overview', 'me', 'tag-assignments'] as const

export function invalidateAccountQueries(queryClient: QueryClient): void {
  for (const key of AFFECTED_QUERY_KEYS) void queryClient.invalidateQueries({ queryKey: [key] })
}
