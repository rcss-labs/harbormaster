import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import { AuthProvider } from '../auth/AuthProvider'
import { ToastProvider } from '../../components/Toast'
import { ShellStateProvider, useShellState } from '../shell/ShellState'
import { AgentDrawer } from './AgentDrawer'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { useEffect } from 'react'
import { mockAgent, openStream, STATUS_OK } from './agentTestKit'

beforeEach(() => {
  localStorage.clear()
})

const openDrawer = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(await screen.findByRole('button', { name: 'Ask AI' }))
  return screen.findByRole('dialog', { name: 'Agent' })
}

const CONVERSATIONS = [
  { id: 2, title: 'Why is payments slow', updated_at: '2026-11-24T10:00:00Z' },
  { id: 1, title: 'Old question', updated_at: '2026-11-20T10:00:00Z' },
]
const message = (id: number, role: 'user' | 'assistant', text: string, extra: Partial<{ interrupted: boolean; tools: string[] }> = {}) => ({
  id,
  role,
  text,
  tools: [],
  interrupted: false,
  created_at: '2026-11-24T10:00:00Z',
  ...extra,
})
const DETAILS = [
  { id: 2, title: 'Why is payments slow', messages: [message(1, 'user', 'Why is payments slow?'), message(2, 'assistant', 'CPU is saturated.', { tools: ['get_metrics'] })] },
  { id: 1, title: 'Old question', messages: [message(3, 'user', 'Old question text'), message(4, 'assistant', 'Half an ans', { interrupted: true })] },
]

describe('AgentDrawer', () => {
  it('button hidden when can_chat is false', async () => {
    let asked = false
    mockAgent({ status: { ...STATUS_OK, can_chat: false } })
    server.use(
      http.get('/api/agent/status', () => {
        asked = true
        return HttpResponse.json({ ...STATUS_OK, can_chat: false })
      }),
    )
    renderApp('/clusters')
    await screen.findByRole('banner')
    await waitFor(() => expect(asked).toBe(true))
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('button', { name: 'Ask AI' })).not.toBeInTheDocument()
  })

  it('streams text and shows tool names', async () => {
    const stream = openStream()
    mockAgent()
    server.use(
      http.post('/api/agent/conversations', () => HttpResponse.json({ id: 5, title: '' })),
      http.post('/api/agent/conversations/5/messages', () => stream.response()),
    )
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    const input = await within(dialog).findByRole('textbox', { name: 'Message' })
    await user.type(input, 'what is wrong?{Enter}')

    stream.send('tool_start', { name: 'list_services' })
    expect(await within(dialog).findByText('list_services')).toBeInTheDocument()
    expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeDisabled()
    stream.send('text', { delta: 'All **good** ' })
    stream.send('text', { delta: 'here.' })
    expect(await within(dialog).findByText('good')).toBeInTheDocument()
    expect(dialog).toHaveTextContent('All good here.')
    stream.send('notice', { kind: 'context_trimmed' })
    expect(await within(dialog).findByText("Earlier messages were left out to fit the model's limit.")).toBeInTheDocument()
    stream.send('done', { message_id: 9 })
    stream.close()
    await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
  })

  it('shows the budget message', async () => {
    mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
    server.use(
      http.post('/api/agent/conversations/2/messages', () =>
        HttpResponse.json({ error: { code: 'budget_exhausted', message: 'The monthly budget is used up.' } }, { status: 402 }),
      ),
    )
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    await within(dialog).findByText('CPU is saturated.')
    await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'more{Enter}')
    expect(await within(dialog).findByText('The monthly budget is used up.')).toBeInTheDocument()
    expect(within(dialog).getByRole('textbox', { name: 'Message' })).toHaveValue('more')
  })

  it.each([
    ['interrupted', 'Stopped before finishing.'],
    ['timeout', 'The agent took too long and was stopped.'],
  ])('words the %s error code', async (code, copy) => {
    const stream = openStream()
    mockAgent()
    server.use(
      http.post('/api/agent/conversations', () => HttpResponse.json({ id: 5, title: '' })),
      http.post('/api/agent/conversations/5/messages', () => stream.response()),
    )
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    await user.type(await within(dialog).findByRole('textbox', { name: 'Message' }), 'hello{Enter}')
    stream.send('error', { code, message: 'raw server text' })
    stream.close()
    expect(await within(dialog).findByText(copy)).toBeInTheDocument()
    expect(within(dialog).queryByText('raw server text')).not.toBeInTheDocument()
  })

  it('stop aborts and re-enables the input', async () => {
    const stream = openStream()
    mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
    server.use(http.post('/api/agent/conversations/2/messages', () => stream.response()))
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    await within(dialog).findByText('CPU is saturated.')
    await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'go{Enter}')
    stream.send('text', { delta: 'partial' })
    await within(dialog).findByText('partial')
    await user.click(within(dialog).getByRole('button', { name: 'Stop' }))
    await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
    expect(within(dialog).queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument()
    expect(within(dialog).queryByText(/unavailable/)).not.toBeInTheDocument()
  })

  describe('after Stop or a failed refetch', () => {
    const savedUser = message(40, 'user', 'go')
    const savedAnswer = message(41, 'assistant', 'The saved answer.', { interrupted: true })

    /** A conversation 2 whose GET answers by `phase`: 'old' is as before the question, 'down' fails, 'saved' holds the new answer. */
    const mockConversation = (phase: () => 'old' | 'down' | 'saved') => {
      let reads = 0
      server.use(
        http.get('/api/agent/conversations/2', () => {
          reads++
          const now = phase()
          if (now === 'down') return HttpResponse.json({ error: { code: 'internal', message: 'Boom.' } }, { status: 500 })
          return HttpResponse.json({ ...DETAILS[0], messages: now === 'saved' ? [...DETAILS[0].messages, savedUser, savedAnswer] : DETAILS[0].messages })
        }),
      )
      return () => reads
    }

    const startAndStop = async (stream: ReturnType<typeof openStream>) => {
      const user = userEvent.setup()
      renderApp('/clusters')
      const dialog = await openDrawer(user)
      await within(dialog).findByText('CPU is saturated.')
      await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'go{Enter}')
      stream.send('text', { delta: 'partial answer' })
      await within(dialog).findByText('partial answer')
      await user.click(within(dialog).getByRole('button', { name: 'Stop' }))
      return dialog
    }

    it('keeps the partial answer when the server saves late, then shows the saved one once', async () => {
      const stream = openStream()
      let saved = false
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      mockConversation(() => (saved ? 'saved' : 'old'))
      server.use(http.post('/api/agent/conversations/2/messages', () => stream.response()))
      const dialog = await startAndStop(stream)
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      expect(within(dialog).getByText('partial answer')).toBeInTheDocument()
      expect(within(dialog).getByText('Stopped before finishing.')).toBeInTheDocument()
      saved = true
      expect(await within(dialog).findByText('The saved answer.', {}, { timeout: 4000 })).toBeInTheDocument()
      expect(within(dialog).queryByText('partial answer')).not.toBeInTheDocument()
      expect(within(dialog).getAllByText('go')).toHaveLength(1)
      expect(within(dialog).getAllByText('The saved answer.')).toHaveLength(1)
      // The saved message carries its own "stopped" note: one, not two.
      expect(within(dialog).getAllByText('Stopped before finishing.')).toHaveLength(1)
    })

    it('keeps the partial answer when the refetch fails, and recovers on the delayed one', async () => {
      const stream = openStream()
      let phase: 'old' | 'down' | 'saved' = 'down'
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      server.use(http.post('/api/agent/conversations/2/messages', () => stream.response()))
      const dialog = await startAndStop(stream)
      // The first read is the one before the question; from now on reads fail.
      mockConversation(() => phase)
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      expect(within(dialog).getByText('partial answer')).toBeInTheDocument()
      expect(within(dialog).getByText('Stopped before finishing.')).toBeInTheDocument()
      phase = 'saved'
      expect(await within(dialog).findByText('The saved answer.', {}, { timeout: 4000 })).toBeInTheDocument()
      expect(within(dialog).queryByText('partial answer')).not.toBeInTheDocument()
    })

    /** Conversation 2 whose GET can be held back, and whose POSTs answer with the streams in order. */
    const mockTwoTurns = (streams: ReturnType<typeof openStream>[]) => {
      const state = { saved: false, hold: false, held: 0, release: () => {} }
      let posts = 0
      server.use(
        http.get('/api/agent/conversations/2', async () => {
          if (state.hold) {
            state.held++
            await new Promise<void>((resolve) => {
              state.release = resolve
            })
          }
          return HttpResponse.json({ ...DETAILS[0], messages: state.saved ? [...DETAILS[0].messages, savedUser, savedAnswer] : DETAILS[0].messages })
        }),
        http.post('/api/agent/conversations/2/messages', () => streams[posts++].response()),
      )
      return state
    }

    it('a recheck that was hanging when the next question started does not clear that question', async () => {
      const [first, second] = [openStream(), openStream()]
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      const server2 = mockTwoTurns([first, second])
      const dialog = await startAndStop(first)
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      server2.hold = true
      await waitFor(() => expect(server2.held).toBe(1), { timeout: 4000 }) // the recheck is asking, and waits
      const user = userEvent.setup()
      await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'again{Enter}')
      second.send('text', { delta: 'second partial' })
      await within(dialog).findByText('second partial')
      server2.saved = true // the first answer is saved: not the second
      server2.hold = false
      server2.release()
      await new Promise((resolve) => setTimeout(resolve, 300))
      expect(within(dialog).getByText('second partial')).toBeInTheDocument()
      expect(within(dialog).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    })

    it('a second Stop before the first answer is saved keeps its own partial', async () => {
      const [first, second] = [openStream(), openStream()]
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      const state = mockTwoTurns([first, second])
      const dialog = await startAndStop(first)
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      const user = userEvent.setup()
      await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'again{Enter}')
      second.send('text', { delta: 'second partial' })
      await within(dialog).findByText('second partial')
      state.saved = true // only the first answer is saved
      await user.click(within(dialog).getByRole('button', { name: 'Stop' }))
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      expect(within(dialog).getByText('second partial')).toBeInTheDocument()
      expect(within(dialog).getByText('Stopped before finishing.')).toBeInTheDocument()
    })

    it('a later read that holds the answer clears the kept partial', async () => {
      const stream = openStream()
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      const state = mockTwoTurns([stream])
      const dialog = await startAndStop(stream)
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      expect(within(dialog).getByText('partial answer')).toBeInTheDocument()
      state.saved = true
      window.dispatchEvent(new Event('visibilitychange')) // a background refetch, well before the 1.5 s recheck
      expect(await within(dialog).findByText('The saved answer.', {}, { timeout: 1000 })).toBeInTheDocument()
      expect(within(dialog).queryByText('partial answer')).not.toBeInTheDocument()
    })

    it('deleting the conversation drops the kept partial', async () => {
      const stream = openStream()
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      mockTwoTurns([stream])
      server.use(http.delete('/api/agent/conversations/2', () => new HttpResponse(null, { status: 204 })))
      const dialog = await startAndStop(stream)
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      const user = userEvent.setup()
      await user.click(within(dialog).getByRole('button', { name: 'Conversations' }))
      await user.click(await within(dialog).findByRole('button', { name: 'Delete Why is payments slow' }))
      const confirm = await screen.findByRole('dialog', { name: /Delete/ })
      await user.click(within(confirm).getByRole('button', { name: 'Delete' }))
      await waitFor(() => expect(screen.queryByRole('dialog', { name: /Delete/ })).not.toBeInTheDocument())
      await user.click(within(dialog).getByRole('button', { name: 'Back to chat' }))
      expect(within(dialog).queryByText('partial answer')).not.toBeInTheDocument()
    })

    it('a normal end shows the saved answer at once, with no stopped note', async () => {
      const stream = openStream()
      let saved = false
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      mockConversation(() => (saved ? 'saved' : 'old'))
      server.use(http.post('/api/agent/conversations/2/messages', () => stream.response()))
      const user = userEvent.setup()
      renderApp('/clusters')
      const dialog = await openDrawer(user)
      await within(dialog).findByText('CPU is saturated.')
      await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'go{Enter}')
      stream.send('text', { delta: 'partial answer' })
      await within(dialog).findByText('partial answer')
      saved = true
      stream.send('done', { message_id: 41 })
      stream.close()
      expect(await within(dialog).findByText('The saved answer.')).toBeInTheDocument()
      expect(within(dialog).queryByText('partial answer')).not.toBeInTheDocument()
      expect(within(dialog).getAllByText('go')).toHaveLength(1)
    })
  })

  it('lists and reopens a conversation', async () => {
    mockAgent({ conversations: CONVERSATIONS, details: DETAILS })
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    expect(await within(dialog).findByText('CPU is saturated.')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Conversations' }))
    expect(await within(dialog).findByRole('button', { name: 'New conversation' })).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Open Old question' }))
    expect(await within(dialog).findByText('Old question text')).toBeInTheDocument()
    expect(within(dialog).getByText('Stopped before finishing.')).toBeInTheDocument()
  })

  it('deletes after confirm', async () => {
    mockAgent({ conversations: CONVERSATIONS, details: DETAILS })
    let deleted = ''
    server.use(
      http.delete('/api/agent/conversations/:id', ({ params }) => {
        deleted = String(params.id)
        return new HttpResponse(null, { status: 204 })
      }),
    )
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    await within(dialog).findByText('CPU is saturated.')
    await user.click(within(dialog).getByRole('button', { name: 'Conversations' }))
    await user.click(await within(dialog).findByRole('button', { name: 'Delete Old question' }))
    expect(deleted).toBe('')
    const confirm = await screen.findByRole('dialog', { name: /Delete/ })
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleted).toBe('1'))
  })

  it('sends the page context of a service page', async () => {
    let sent: unknown
    mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
    server.use(
      http.post('/api/agent/conversations/2/messages', async ({ request }) => {
        sent = await request.json()
        const stream = openStream()
        queueMicrotask(() => {
          stream.send('done', { message_id: 1 })
          stream.close()
        })
        return stream.response()
      }),
    )
    const user = userEvent.setup()
    renderApp(`/services/${encodeURIComponent(SERVICE_PAYMENTS.id)}/overview`)
    const dialog = await openDrawer(user)
    expect(await within(dialog).findByText(`Looking at: ${SERVICE_PAYMENTS.name}`)).toBeInTheDocument()
    await within(dialog).findByText('CPU is saturated.')
    await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'status?{Enter}')
    await waitFor(() => expect(sent).toBeDefined())
    expect(sent).toEqual({ text: 'status?', page: { path: `/services/${encodeURIComponent(SERVICE_PAYMENTS.id)}/overview`, service_id: SERVICE_PAYMENTS.id } })
  })

  describe('while a turn is live', () => {
    const savedUser = message(40, 'user', 'live question')
    const savedAnswer = message(41, 'assistant', 'streamed answer')
    const refocus = () => window.dispatchEvent(new Event('visibilitychange'))

    it('shows the question once for an existing conversation, even when a refetch returns it', async () => {
      const stream = openStream()
      let finished = false
      let posted = false
      let reads = 0
      mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: DETAILS })
      server.use(
        http.get('/api/agent/conversations/2', () => {
          reads++
          const messages = [...DETAILS[0].messages, ...(posted ? [savedUser] : []), ...(finished ? [savedAnswer] : [])]
          return HttpResponse.json({ ...DETAILS[0], messages })
        }),
        http.post('/api/agent/conversations/2/messages', () => {
          posted = true // the server keeps the question before the first byte
          return stream.response()
        }),
      )
      const user = userEvent.setup()
      renderApp('/clusters')
      const dialog = await openDrawer(user)
      await within(dialog).findByText('CPU is saturated.')
      await user.type(within(dialog).getByRole('textbox', { name: 'Message' }), 'live question{Enter}')
      stream.send('text', { delta: 'streamed answer' })
      await within(dialog).findByText('streamed answer')
      const before = reads
      refocus()
      await waitFor(() => expect(reads).toBeGreaterThan(before))
      await new Promise((resolve) => setTimeout(resolve, 50))
      expect(within(dialog).getAllByText('live question')).toHaveLength(1)
      expect(within(dialog).getAllByText('streamed answer')).toHaveLength(1)
      finished = true
      stream.send('done', { message_id: 41 })
      stream.close()
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      expect(within(dialog).getAllByText('live question')).toHaveLength(1)
      expect(within(dialog).getAllByText('streamed answer')).toHaveLength(1)
    })

    it('shows the question once and keeps the text for a new conversation whose fetch returns it', async () => {
      const stream = openStream()
      let finished = false
      let reads = 0
      mockAgent()
      server.use(
        http.post('/api/agent/conversations', () => HttpResponse.json({ id: 5, title: '' })),
        http.get('/api/agent/conversations/5', () => {
          reads++
          return HttpResponse.json({ id: 5, title: 't', messages: [savedUser, ...(finished ? [savedAnswer] : [])] })
        }),
        http.post('/api/agent/conversations/5/messages', () => stream.response()),
      )
      const user = userEvent.setup()
      renderApp('/clusters')
      const dialog = await openDrawer(user)
      await user.type(await within(dialog).findByRole('textbox', { name: 'Message' }), 'live question{Enter}')
      await waitFor(() => expect(reads).toBeGreaterThan(0))
      stream.send('text', { delta: 'streamed answer' })
      await within(dialog).findByText('streamed answer')
      refocus()
      await new Promise((resolve) => setTimeout(resolve, 50))
      expect(within(dialog).getAllByText('live question')).toHaveLength(1)
      expect(within(dialog).getAllByText('streamed answer')).toHaveLength(1)
      finished = true
      stream.send('done', { message_id: 41 })
      stream.close()
      await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Message' })).toBeEnabled())
      expect(within(dialog).getAllByText('live question')).toHaveLength(1)
      expect(within(dialog).getAllByText('streamed answer')).toHaveLength(1)
    })
  })

  it('says when a conversation no longer exists', async () => {
    mockAgent({ conversations: CONVERSATIONS.slice(0, 1), details: [] })
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    expect(await within(dialog).findByText('This conversation no longer exists.')).toBeInTheDocument()
  })

  it('says when the conversations could not be loaded', async () => {
    mockAgent()
    server.use(http.get('/api/agent/conversations', () => HttpResponse.json({ error: { code: 'x', message: 'boom' } }, { status: 500 })))
    const user = userEvent.setup()
    renderApp('/clusters')
    const dialog = await openDrawer(user)
    expect(await within(dialog).findByText('Could not load the conversation. Try again.')).toBeInTheDocument()
  })

  it('renders nothing and asks for nothing when the agent is not available', async () => {
    let asked = 0
    mockAgent({ status: { ...STATUS_OK, can_chat: false } })
    server.use(
      http.get('/api/agent/conversations', () => {
        asked++
        return HttpResponse.json({ items: [] })
      }),
    )
    function OpenOnMount() {
      const { openAgent } = useShellState()
      useEffect(openAgent, [openAgent])
      return null
    }
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <ToastProvider>
            <AuthProvider>
              <ShellStateProvider>
                <OpenOnMount />
                <AgentDrawer />
              </ShellStateProvider>
            </AuthProvider>
          </ToastProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 300))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(asked).toBe(0)
  })
})
