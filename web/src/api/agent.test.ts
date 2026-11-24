import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/msw'
import { ApiError, setCsrfToken } from './client'
import { sendMessage, type AgentStreamHandlers } from './agent'

const encoder = new TextEncoder()
const sse = (chunks: string[], delayMs = 0) =>
  new HttpResponse(
    new ReadableStream({
      async start(controller) {
        for (const chunk of chunks) {
          controller.enqueue(encoder.encode(chunk))
          if (delayMs) await new Promise((resolve) => setTimeout(resolve, delayMs))
        }
        controller.close()
      },
    }),
    { headers: { 'Content-Type': 'text/event-stream' } },
  )

const PAGE = { path: '/services', service_id: undefined, cluster_id: undefined }
const handlers = (): AgentStreamHandlers => ({
  onText: vi.fn(),
  onToolStart: vi.fn(),
  onToolEnd: vi.fn(),
  onNotice: vi.fn(),
  onDone: vi.fn(),
  onError: vi.fn(),
})

describe('sendMessage', () => {
  it('parses a multi-event SSE chunk', async () => {
    setCsrfToken('csrf-test')
    let csrf: string | null = null
    let body: unknown
    server.use(
      http.post('/api/agent/conversations/7/messages', async ({ request }) => {
        csrf = request.headers.get('X-CSRF-Token')
        body = await request.json()
        return sse([
          ': keep-alive\n\n' +
            'event: tool_start\ndata: {"name":"list_services"}\n\n' +
            'event: tool_end\ndata: {"name":"list_services","ok":true}\n\n' +
            'event: mystery\ndata: {"x":1}\n\n' +
            'event: notice\ndata: {"kind":"context_trimmed"}\n\n' +
            'event: text\ndata: {"delta":"Hello"}\n\n' +
            'event: done\ndata: {"message_id":12}\n\n',
        ])
      }),
    )
    const h = handlers()
    await sendMessage(7, 'hi', PAGE, h, new AbortController().signal)
    expect(csrf).toBe('csrf-test')
    expect(body).toEqual({ text: 'hi', page: { path: '/services' } })
    expect(h.onToolStart).toHaveBeenCalledWith('list_services')
    expect(h.onToolEnd).toHaveBeenCalledWith('list_services', true)
    expect(h.onNotice).toHaveBeenCalledWith('context_trimmed')
    expect(h.onText).toHaveBeenCalledWith('Hello')
    expect(h.onDone).toHaveBeenCalledWith(12)
    expect(h.onError).not.toHaveBeenCalled()
  })

  it('handles an event split across chunks', async () => {
    server.use(
      http.post('/api/agent/conversations/7/messages', () =>
        sse(['event: te', 'xt\r\ndata: {"delta":"He', 'llo"}\r\n', '\r\nevent: done\ndata: {"message_id":3}\n'], 5),
      ),
    )
    const h = handlers()
    await sendMessage(7, 'hi', PAGE, h, new AbortController().signal)
    expect(h.onText).toHaveBeenCalledTimes(1)
    expect(h.onText).toHaveBeenCalledWith('Hello')
    expect(h.onDone).toHaveBeenCalledWith(3) // the last event has no blank line after it
  })

  it('aborts on signal', async () => {
    server.use(
      http.post('/api/agent/conversations/7/messages', () =>
        sse(['event: text\ndata: {"delta":"one"}\n\n', 'event: text\ndata: {"delta":"two"}\n\n'], 300),
      ),
    )
    const h = handlers()
    const controller = new AbortController()
    ;(h.onText as ReturnType<typeof vi.fn>).mockImplementation(() => controller.abort())
    await expect(sendMessage(7, 'hi', PAGE, h, controller.signal)).resolves.toBeUndefined()
    expect(h.onText).toHaveBeenCalledTimes(1)
    expect(h.onDone).not.toHaveBeenCalled()
  })

  it('maps a JSON error response before the stream', async () => {
    server.use(
      http.post('/api/agent/conversations/7/messages', () =>
        HttpResponse.json({ error: { code: 'budget_exhausted', message: 'The monthly budget is used up.' } }, { status: 402 }),
      ),
    )
    const h = handlers()
    const failure = await sendMessage(7, 'hi', PAGE, h, new AbortController().signal).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(ApiError)
    expect(failure).toMatchObject({ code: 'budget_exhausted', status: 402, message: 'The monthly budget is used up.' })
  })

  it('treats a stream that ends without done as unavailable', async () => {
    server.use(http.post('/api/agent/conversations/7/messages', () => sse(['event: text\ndata: {"delta":"par"}\n\n'])))
    const failure = await sendMessage(7, 'hi', PAGE, handlers(), new AbortController().signal).catch((error: unknown) => error)
    expect(failure).toMatchObject({ code: 'agent_unavailable' })
  })
})
