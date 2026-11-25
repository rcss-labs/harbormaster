import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '../app/auth/AuthProvider'
import { api, ApiError, getCsrfToken } from './client'
import type { AgentConversationDTO, AgentConversationSummaryDTO, AgentPageContext, AgentStatusDTO } from './types'

const STATUS_KEY = ['agent', 'status'] as const
export const CONVERSATIONS_KEY = ['agent', 'conversations'] as const
export const conversationKey = (id: number) => ['agent', 'conversation', id] as const

/** The agent is switched on for this server and this user. The status call is made only then. */
export function useAgentStatus() {
  const { me } = useAuth()
  const allowed = Boolean(me?.agent_enabled && me.ai_chat)
  return useQuery({
    queryKey: STATUS_KEY,
    queryFn: () => api.get<AgentStatusDTO>('/api/agent/status'),
    enabled: allowed,
    staleTime: 30_000,
  })
}

/** True when the "Ask AI" button should show. */
export function useAgentAvailable(): boolean {
  const status = useAgentStatus().data
  return Boolean(status?.enabled && status.can_chat)
}

export function useConversations() {
  return useQuery({
    queryKey: CONVERSATIONS_KEY,
    queryFn: () => api.get<{ items: AgentConversationSummaryDTO[] }>('/api/agent/conversations'),
  })
}

export function useConversation(id: number | null) {
  return useQuery({
    queryKey: conversationKey(id ?? 0),
    queryFn: () => api.get<AgentConversationDTO>(`/api/agent/conversations/${id}`),
    enabled: id !== null,
  })
}

export const createConversation = () => api.post<{ id: number; title: string }>('/api/agent/conversations')
export const deleteConversation = (id: number) => api.del(`/api/agent/conversations/${id}`)

export function useDeleteConversation() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: deleteConversation,
    onSuccess: (_result, id) => {
      queryClient.removeQueries({ queryKey: conversationKey(id) })
      void queryClient.invalidateQueries({ queryKey: CONVERSATIONS_KEY })
    },
  })
}

export interface AgentStreamHandlers {
  onText(delta: string): void
  onToolStart(name: string): void
  onToolEnd(name: string, ok: boolean): void
  onNotice(kind: string): void
  onDone(messageId: number): void
  onError(code: string, message: string): void
}

const UNAVAILABLE = 'The agent is unavailable. Try again shortly.'

function dispatchEvent(type: string, data: unknown, handlers: AgentStreamHandlers): 'done' | 'error' | null {
  const body = (data && typeof data === 'object' ? data : {}) as Record<string, unknown>
  switch (type) {
    case 'text':
      if (typeof body.delta === 'string') handlers.onText(body.delta)
      return null
    case 'tool_start':
      if (typeof body.name === 'string') handlers.onToolStart(body.name)
      return null
    case 'tool_end':
      if (typeof body.name === 'string') handlers.onToolEnd(body.name, body.ok === true)
      return null
    case 'notice':
      if (typeof body.kind === 'string') handlers.onNotice(body.kind)
      return null
    case 'done':
      handlers.onDone(typeof body.message_id === 'number' ? body.message_id : 0)
      return 'done'
    case 'error':
      handlers.onError(typeof body.code === 'string' ? body.code : 'agent_unavailable', typeof body.message === 'string' ? body.message : UNAVAILABLE)
      return 'error'
    default:
      return null // an event type this version does not know
  }
}

/** One SSE frame (the lines between blank lines) into an event type and its parsed JSON data, or null for comments and junk. */
function parseFrame(frame: string): { type: string; data: unknown } | null {
  let type = 'message'
  const dataLines: string[] = []
  for (const line of frame.split('\n')) {
    if (line === '' || line.startsWith(':')) continue
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    const value = colon === -1 ? '' : line.slice(colon + 1).replace(/^ /, '')
    if (field === 'event') type = value
    else if (field === 'data') dataLines.push(value)
  }
  if (dataLines.length === 0) return null
  try {
    return { type, data: JSON.parse(dataLines.join('\n')) }
  } catch {
    return null
  }
}

/**
 * Posts a question and reads the answer as server-sent events from the response body (a POST, so not EventSource).
 * Resolves when the stream ends with `done` or `error`, or quietly when `signal` aborts. Rejects with ApiError for a JSON
 * error before the stream and for a stream that drops without finishing.
 */
export async function sendMessage(
  conversationId: number,
  text: string,
  page: AgentPageContext,
  handlers: AgentStreamHandlers,
  signal: AbortSignal,
): Promise<void> {
  const headers = new Headers({ 'Content-Type': 'application/json', Accept: 'text/event-stream' })
  const csrf = getCsrfToken()
  if (csrf) headers.set('X-CSRF-Token', csrf)
  let finished = false
  let handlerThrew = false
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
  try {
    const response = await fetch(`/api/agent/conversations/${conversationId}/messages`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ text, page }),
      credentials: 'same-origin',
      signal,
    })
    if (!response.ok) {
      let code = 'unknown'
      let message = response.statusText || 'Request failed.'
      try {
        const body = (await response.json()) as { error?: { code?: string; message?: string } }
        if (body.error?.code) code = body.error.code
        if (body.error?.message) message = body.error.message
      } catch {
        // Non-JSON error body: keep the defaults.
      }
      if (response.status === 401) window.dispatchEvent(new Event('hm:unauthenticated'))
      throw new ApiError(code, message, response.status)
    }
    if (!response.body) throw new ApiError('agent_unavailable', UNAVAILABLE, 503)

    reader = response.body.getReader()
    const activeReader = reader
    const decoder = new TextDecoder()
    let buffer = ''
    // A handler's own failure is not a stream failure: it passes through unchanged.
    const deliver = (frame: { type: string; data: unknown }) => {
      try {
        return dispatchEvent(frame.type, frame.data, handlers) !== null
      } catch (error) {
        handlerThrew = true
        throw error
      }
    }
    const drain = (final: boolean) => {
      // A CR at the very end may be half of a CRLF split across chunks, so it waits for the next chunk.
      buffer = buffer.replace(/\r\n/g, '\n').replace(final ? /\r/g : /\r(?!$)/g, '\n')
      let end: number
      while ((end = buffer.indexOf('\n\n')) !== -1) {
        const frame = parseFrame(buffer.slice(0, end))
        buffer = buffer.slice(end + 2)
        if (frame && deliver(frame)) finished = true
        if (finished) return
      }
      if (final && buffer.trim() !== '') {
        const frame = parseFrame(buffer)
        buffer = ''
        if (frame && deliver(frame)) finished = true
      }
    }
    for (;;) {
      const { done, value } = await activeReader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      drain(false)
      if (finished) {
        await activeReader.cancel().catch(() => {}) // nothing after done or error is wanted
        return
      }
    }
    buffer += decoder.decode()
    drain(true)
  } catch (error) {
    void reader?.cancel().catch(() => {})
    if (handlerThrew) throw error
    if (signal.aborted) return
    if (error instanceof ApiError) throw error
    throw new ApiError('agent_unavailable', UNAVAILABLE, 503)
  }
  if (signal.aborted) return
  if (!finished) throw new ApiError('agent_unavailable', UNAVAILABLE, 503)
}

// ---- Admin settings and usage ----

export interface AgentCatalogueModel {
  id: string
  input_per_mtok_micro_usd: number
  output_per_mtok_micro_usd: number
  context_tokens: number
}
export interface AgentChatModel {
  provider: string
  model: string
  /** False when the operator no longer offers this model. */
  available: boolean
  /** What one question reserves with this model, in dollars rounded up; empty while the model is not known. */
  max_question_usd: string
  /** The allowance left this month cannot cover that reservation, so every question is refused. */
  allowance_below_question: boolean
}
export interface AgentSettingsDTO {
  /** Echoed back on save; a save made from an older version is refused with 409 `settings_changed`. */
  version: number
  monthly_budget_usd: string
  unredacted_providers: string[]
  role_models: { chat?: AgentChatModel; investigator?: AgentChatModel }
  catalogue: { providers: { provider: string; models: AgentCatalogueModel[] }[] }
  agent_reachable: boolean
  spend: { month: string; settled_usd: string; reserved_usd: string }
  identity: AgentIdentityDTO
  /** Read-only: set by the operator's configuration. */
  investigator: AgentInvestigatorLimits
}
export interface AgentIdentityPermission {
  tag_id: number
  tag_name: string
  /** View and logs; false is View only. A tag without an entry gives no access. */
  logs: boolean
}
export interface AgentIdentityDTO {
  username: string
  permissions: AgentIdentityPermission[]
}
export interface AgentInvestigatorLimits {
  window_minutes: number
  per_hour: number
  concurrent: number
  max_wait_minutes: number
}
export type InvestigationState = 'requested' | 'running' | 'done' | 'failed' | 'skipped'
export interface AgentInvestigationDTO {
  id: number
  state: InvestigationState
  reason: string
  subject: { kind: 'service' | 'cluster' | 'account'; service_id: string; service: string; cluster: string; cluster_id: string; account_id: string; region: string }
  alert: { kind: string; title: string }
  requested_at: string
  started_at: string | null
  finished_at: string | null
  summary: string
  summary_replaced: boolean
  provider: string
  model: string
  attempts: number
  cost_usd: string
}
export interface AgentInvestigationsPage {
  items: AgentInvestigationDTO[]
  /** Empty on the last page. */
  next_cursor: string
}
export interface AgentSettingsUpdate {
  version: number
  monthly_budget_usd: string
  unredacted_providers: string[]
  role_models: { chat?: { provider: string; model: string }; investigator?: { provider: string; model: string } }
}
export interface AgentTestResult {
  ok: boolean
  kind: string
  message: string
}
export interface AgentUsageDTO {
  month: string
  days: { day: string; role: string; runs: number; cost_usd: string; input_tokens: number; output_tokens: number }[]
}

const SETTINGS_KEY = ['agent', 'settings'] as const

export function useAgentSettings(enabled = true) {
  return useQuery({ queryKey: SETTINGS_KEY, queryFn: () => api.get<AgentSettingsDTO>('/api/agent/settings'), enabled })
}

export function useSaveAgentSettings() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: AgentSettingsUpdate) => api.put<AgentSettingsDTO>('/api/agent/settings', body),
    onSuccess: (saved) => {
      // The identity has its own save; a settings answer made before it must not put the old access back.
      queryClient.setQueryData<AgentSettingsDTO>(SETTINGS_KEY, (current) => (current ? { ...saved, identity: current.identity } : saved))
      void queryClient.invalidateQueries({ queryKey: STATUS_KEY })
    },
  })
}

export function useSaveAgentIdentity() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: { permissions: { tag_id: number; logs: boolean }[] }) => api.put<AgentIdentityDTO>('/api/agent/identity', body),
    onSuccess: (identity) => {
      queryClient.setQueryData<AgentSettingsDTO>(SETTINGS_KEY, (current) => (current ? { ...current, identity } : current))
    },
  })
}

/** Past investigations, newest first, a page at a time. */
export function useAgentInvestigations() {
  return useInfiniteQuery({
    queryKey: ['agent', 'investigations'],
    queryFn: ({ pageParam }) => api.get<AgentInvestigationsPage>(`/api/agent/investigations${pageParam ? `?cursor=${encodeURIComponent(pageParam)}` : ''}`),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
  })
}

export function useTestAgentModel() {
  return useMutation({
    mutationFn: (body: { provider: string; model: string }) => api.post<AgentTestResult>('/api/agent/settings/test', body),
  })
}

export function useAgentUsage() {
  return useQuery({ queryKey: ['agent', 'usage'], queryFn: () => api.get<AgentUsageDTO>('/api/agent/usage') })
}

export const setUserAiChat = (userId: number, on: boolean) => api.post(`/api/users/${userId}/ai-chat`, { on })
