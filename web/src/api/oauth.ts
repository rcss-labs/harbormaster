// The OAuth consent page's calls and the connected apps' revoke calls. They mirror internal/api/oauth.go and
// internal/api/tokens.go (JSON is snake_case).
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from './client'

/** An authorization request's parameters, named as in OAuth. */
export interface AuthorizeRequest {
  response_type: string
  client_id: string
  redirect_uri: string
  code_challenge: string
  code_challenge_method: string
  scope: string
  state: string
  resource: string
}

/** What the consent page shows. */
export interface AuthorizeViewDTO {
  client_name: string
  client_host: string
  redirect_host: string
  scope: string
  read_only: boolean
  expires_in_days: number
  sees_logs: boolean
  username: string
}

/** A refused request. redirect_to is present only when the client and its redirect URI were validated. */
export interface OAuthErrorDTO {
  error: string
  error_description: string
  redirect_to?: string
}

const REQUEST_KEYS: (keyof AuthorizeRequest)[] = [
  'response_type',
  'client_id',
  'redirect_uri',
  'code_challenge',
  'code_challenge_method',
  'scope',
  'state',
  'resource',
]

/** Reads the OAuth parameters from a query string; anything else in it is ignored. */
export function authorizeRequestFrom(search: string): AuthorizeRequest {
  const params = new URLSearchParams(search)
  const request = {} as AuthorizeRequest
  for (const key of REQUEST_KEYS) request[key] = params.get(key) ?? ''
  return request
}

function queryOf(request: AuthorizeRequest): string {
  const params = new URLSearchParams()
  for (const key of REQUEST_KEYS) params.set(key, request[key])
  return params.toString()
}

/** The OAuth error carried by a failed call, or a generic one for anything else (network, 5xx, 401). */
export function oauthErrorOf(error: unknown): OAuthErrorDTO {
  if (error instanceof ApiError) {
    const body = error.body as Partial<OAuthErrorDTO> | undefined
    if (body && typeof body.error === 'string' && typeof body.error_description === 'string') {
      return {
        error: body.error,
        error_description: body.error_description,
        redirect_to: typeof body.redirect_to === 'string' ? body.redirect_to : undefined,
      }
    }
    if (error.status === 404) return { error: 'unavailable', error_description: 'Connecting applications is not turned on in Harbormaster.' }
    return { error: error.code, error_description: error.message }
  }
  return { error: 'unknown', error_description: 'Could not reach Harbormaster.' }
}

/** Only an http(s) URL is ever navigated to; the server already validated it against the client's registration. */
export function safeRedirect(target: string | undefined): string | null {
  if (!target) return null
  try {
    const url = new URL(target)
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.toString() : null
  } catch {
    return null
  }
}

/** Validates the request for the consent page. Never cached: the answer depends on the signed-in user. */
export function useAuthorizeRequest(request: AuthorizeRequest) {
  return useQuery({
    queryKey: ['oauth-authorize', queryOf(request)],
    queryFn: () => api.get<AuthorizeViewDTO>(`/api/oauth/authorize?${queryOf(request)}`),
    retry: false,
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
}

/** Posts the user's decision; the answer is where to send the browser. */
export function useAuthorizeDecision() {
  return useMutation({
    mutationFn: ({ request, decision }: { request: AuthorizeRequest; decision: 'approve' | 'deny' }) =>
      api.post<{ redirect_to: string }>('/api/oauth/authorize', { request, decision }),
    // The answer carries an authorization code: keep it out of the mutation cache.
    gcTime: 0,
  })
}

const TOKENS_KEY = ['tokens'] as const

/** Revokes one of the caller's connected apps. */
export function useRevokeGrant() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.del(`/api/oauth/grants/${id}`),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: TOKENS_KEY })
      void queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })
}

/** Admin: revokes one user's connected app. */
export function useRevokeUserGrant() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ userId, grantId }: { userId: number; grantId: number }) => api.del(`/api/users/${userId}/oauth-grants/${grantId}`),
    onSuccess: (_result, { userId }) => {
      void queryClient.invalidateQueries({ queryKey: ['users', userId, 'tokens'] })
      void queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })
}
