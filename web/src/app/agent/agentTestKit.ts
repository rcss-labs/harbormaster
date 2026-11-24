import { http, HttpResponse } from 'msw'
import { server } from '../../test/msw'
import { ME_ADMIN } from '../../test/fixtures'
import type { AgentConversationDTO, AgentConversationSummaryDTO, AgentStatusDTO } from '../../api/types'

export const STATUS_OK: AgentStatusDTO = { enabled: true, can_chat: true, model_configured: true, budget_exhausted: false }

const encoder = new TextEncoder()

/** An SSE response the test feeds by hand, so it can look at the page between events. */
export function openStream() {
  let controller!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c
    },
    cancel() {},
  })
  return {
    response: () => new HttpResponse(body, { headers: { 'Content-Type': 'text/event-stream' } }),
    send: (event: string, data: unknown) => controller.enqueue(encoder.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)),
    close: () => controller.close(),
  }
}

export interface AgentMocks {
  status?: AgentStatusDTO
  conversations?: AgentConversationSummaryDTO[]
  details?: AgentConversationDTO[]
}

export function mockAgent({ status = STATUS_OK, conversations = [], details = [] }: AgentMocks = {}) {
  server.use(
    http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, ai_chat: true })),
    http.get('/api/agent/status', () => HttpResponse.json(status)),
    http.get('/api/agent/conversations', () => HttpResponse.json({ items: conversations })),
    http.get('/api/agent/conversations/:id', ({ params }) => {
      const found = details.find((detail) => String(detail.id) === params.id)
      return found ? HttpResponse.json(found) : HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })
    }),
  )
}
