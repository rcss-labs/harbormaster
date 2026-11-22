// Types and hooks for personal access tokens. They mirror the Go DTOs in internal/api/tokens.go (JSON is snake_case).
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from './client'

export interface TokenDTO {
  id: number
  public_id: string
  name: string
  kind: string
  scope: string
  auth_source: string
  created_at: string
  expires_at: string
  last_used_at: string | null
  last_used_ip: string
}

/** An OAuth connection ("connected app"). It never carries a token. */
export interface GrantDTO {
  id: number
  client_id: string
  client_name: string
  scope: string
  created_at: string
  expires_at: string
  last_used_at: string | null
}

export interface TokenPolicyDTO {
  max_ttl_days: number
  max_tokens: number
  sso_linked: boolean
  admin: boolean
  /** The MCP gateway's public URL; empty when no gateway is set up. */
  mcp_url: string
  /** Whether connectors may sign in with OAuth (HM_OAUTH). */
  oauth: boolean
  log_search_daily_bytes: number
  log_search_used_bytes: number
  log_search_max_bytes_per_search: number
}

export interface TokenListDTO {
  items: TokenDTO[]
  /** Connected apps; empty while OAuth is off. */
  grants: GrantDTO[]
  policy: TokenPolicyDTO
}

export interface UserTokensDTO {
  items: TokenDTO[]
  grants: GrantDTO[]
}

export interface CreateTokenBody {
  name: string
  expires_in_days: number
}

/** The secret is in this answer and nowhere else: keep it in component state only, never in the query cache or storage. */
export interface CreatedTokenDTO {
  token: TokenDTO
  secret: string
}

const TOKENS_KEY = ['tokens'] as const
const userTokensKey = (userId: number) => ['users', userId, 'tokens'] as const

/** A 404 means tokens are off (D12); callers render nothing for it. */
export const isTokensOff = (error: unknown) => error instanceof ApiError && error.status === 404

/** The caller's own tokens and the policy they are created under. */
export function useTokens() {
  return useQuery({
    queryKey: TOKENS_KEY,
    queryFn: () => api.get<TokenListDTO>('/api/tokens'),
    retry: false,
  })
}

/** Creates a token. The mutation result holds the secret, so callers reset it when the dialog closes. */
export function useCreateToken() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateTokenBody) => api.post<CreatedTokenDTO>('/api/tokens', body),
    // The result carries the secret: do not keep it in the mutation cache once nothing observes it.
    gcTime: 0,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: TOKENS_KEY })
      void queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })
}

export function useRevokeToken() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.del(`/api/tokens/${id}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: TOKENS_KEY })
      void queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })
}

/** Admin: one user's tokens. */
export function useUserTokens(userId: number) {
  return useQuery({
    queryKey: userTokensKey(userId),
    queryFn: () => api.get<UserTokensDTO>(`/api/users/${userId}/tokens`),
    retry: false,
  })
}

export function useRevokeUserToken() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ userId, tokenId }: { userId: number; tokenId: number }) => api.del(`/api/users/${userId}/tokens/${tokenId}`),
    onSuccess: (_result, { userId }) => {
      void queryClient.invalidateQueries({ queryKey: userTokensKey(userId) })
      void queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })
}
