import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { conversationKey, useAgentAvailable, CONVERSATIONS_KEY, createConversation, sendMessage, useConversation, useConversations, useDeleteConversation } from '../../api/agent'
import { ApiError } from '../../api/client'
import type { AgentConversationSummaryDTO, AgentMessageDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Drawer } from '../../components/Drawer'
import { Skeleton } from '../../components/Skeleton'
import { useShellState } from '../shell/ShellState'
import { AnswerText } from './AnswerText'
import { usePageContext } from './usePageContext'
import styles from './agent.module.css'

const UNAVAILABLE = "The agent is unavailable. Try again shortly."

/** User-facing words for an error code; the server's own message is kept only where it carries the detail (the budget). */
function errorCopy(code: string, message: string): string {
  switch (code) {
    case 'budget_exhausted':
      return message
    case 'chat_busy':
      return 'A question is already running in another tab.'
    case 'agent_not_configured':
      return 'No model is set up yet. Ask an admin.'
    case 'agent_unavailable':
      return UNAVAILABLE
    default:
      return message || UNAVAILABLE
  }
}

const LOAD_FAILED = 'Could not load the conversation. Try again.'

const NOTICE_COPY: Record<string, string> = {
  context_trimmed: "Earlier messages were left out to fit the model's limit.",
  limit_reached: 'The agent reached its limit for one question and stopped here.',
}

interface ToolRun {
  name: string
  state: 'running' | 'ok' | 'failed'
}

/** The turn in flight: what the user asked and what has streamed back so far. */
interface LiveTurn {
  /** The conversation as it was when the question was sent. While the turn is live this, not a fetch, is what shows. */
  before: AgentMessageDTO[]
  question: string
  text: string
  tools: ToolRun[]
}

const byNewest = (a: AgentConversationSummaryDTO, b: AgentConversationSummaryDTO) => b.updated_at.localeCompare(a.updated_at)

/** The panel exists only while open, so closing it aborts a running question and the next opening starts from the latest conversation. */
export function AgentDrawer() {
  const { agentOpen } = useShellState()
  const available = useAgentAvailable()
  return agentOpen && available ? <OpenAgentDrawer /> : null
}

function OpenAgentDrawer() {
  const { closeAgent } = useShellState()
  const queryClient = useQueryClient()
  const { page, label } = usePageContext()
  const conversations = useConversations()
  const remove = useDeleteConversation()
  const [view, setView] = useState<'chat' | 'list'>('chat')
  // undefined: not chosen yet, so the most recent conversation; null: a new one, created when the first question is sent.
  const [chosen, setChosen] = useState<number | null | undefined>(undefined)
  const [draft, setDraft] = useState('')
  const [live, setLive] = useState<LiveTurn | null>(null)
  const [notices, setNotices] = useState<string[]>([])
  const [problem, setProblem] = useState<string | null>(null)
  const [toDelete, setToDelete] = useState<AgentConversationSummaryDTO | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)

  const sorted = useMemo(() => [...(conversations.data?.items ?? [])].sort(byNewest), [conversations.data])
  const currentId = chosen !== undefined ? chosen : (sorted[0]?.id ?? null)
  const conversation = useConversation(currentId)
  const running = live !== null
  const loadError = conversation.isError
    ? conversation.error instanceof ApiError && conversation.error.status === 404
      ? 'This conversation no longer exists.'
      : LOAD_FAILED
    : chosen === undefined && conversations.isError
      ? LOAD_FAILED
      : null

  useEffect(() => () => abortRef.current?.abort(), [])
  useEffect(() => {
    const list = scrollRef.current
    if (list) list.scrollTop = list.scrollHeight
  }, [conversation.data, live, notices, problem, view])
  useEffect(() => {
    if (!running && view === 'chat') inputRef.current?.focus()
  }, [running, view])

  const ready = chosen !== undefined || !conversations.isPending

  const send = useCallback(async () => {
    const text = draft.trim()
    if (!text || running) return
    setDraft('')
    setNotices([])
    setProblem(null)
    setLive({ before: conversation.data?.messages ?? [], question: text, text: '', tools: [] })
    const controller = new AbortController()
    abortRef.current = controller
    let id = currentId
    let streamed = false
    try {
      if (id === null) {
        id = (await createConversation()).id
        setChosen(id)
      }
      await sendMessage(
        id,
        text,
        page,
        {
          onText: (delta) => {
            streamed = true
            setLive((turn) => turn && { ...turn, text: turn.text + delta })
          },
          onToolStart: (name) => {
            streamed = true
            setLive((turn) => turn && { ...turn, tools: [...turn.tools, { name, state: 'running' }] })
          },
          onToolEnd: (name, ok) =>
            setLive((turn) => {
              if (!turn) return turn
              const index = turn.tools.findIndex((tool) => tool.name === name && tool.state === 'running')
              if (index === -1) return turn
              const tools = turn.tools.slice()
              tools[index] = { name, state: ok ? 'ok' : 'failed' }
              return { ...turn, tools }
            }),
          onNotice: (kind) => {
            const copy = NOTICE_COPY[kind]
            if (copy) setNotices((current) => (current.includes(copy) ? current : [...current, copy]))
          },
          onDone: () => {},
          onError: (code, message) => setProblem(errorCopy(code, message)),
        },
        controller.signal,
      )
    } catch (error) {
      setProblem(error instanceof ApiError ? errorCopy(error.code, error.message) : error instanceof TypeError ? UNAVAILABLE : 'Something went wrong. Try again.')
      // Nothing came back and the question may not have been kept: give the text back to edit and send again.
      if (!streamed) setDraft(text)
    }
    if (id !== null) {
      await Promise.all([queryClient.invalidateQueries({ queryKey: conversationKey(id) }), queryClient.invalidateQueries({ queryKey: CONVERSATIONS_KEY })])
    }
    abortRef.current = null
    setLive(null)
  }, [draft, running, currentId, page, queryClient, conversation.data])

  const stop = () => abortRef.current?.abort()

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault()
      void send()
    }
  }

  const startNew = () => {
    setChosen(null)
    setNotices([])
    setProblem(null)
    setView('chat')
  }
  const open = (id: number) => {
    setChosen(id)
    setNotices([])
    setProblem(null)
    setView('chat')
  }
  const confirmDelete = () => {
    if (!toDelete) return
    const { id } = toDelete
    remove.mutate(id, {
      onSuccess: () => {
        if (currentId === id) setChosen(null)
        setToDelete(null)
      },
      onError: (error) => {
        setProblem(error instanceof Error ? error.message : 'The conversation could not be deleted.')
        setToDelete(null)
      },
    })
  }

  const composer =
    view === 'chat' ? (
      <div className={styles.composer}>
        <textarea
          ref={inputRef}
          className={styles.input}
          aria-label="Message"
          placeholder="Ask about your services, tasks and logs"
          rows={2}
          value={draft}
          disabled={running}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
        />
        {running ? (
          <button type="button" className={styles.btn} onClick={stop}>
            Stop
          </button>
        ) : (
          <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={draft.trim() === ''} onClick={() => void send()}>
            Send
          </button>
        )}
      </div>
    ) : undefined

  return (
    <>
      <Drawer open onClose={closeAgent} title="Agent" wide footer={composer}>
        <div className={styles.panel}>
          <div className={styles.toolbar}>
            {view === 'chat' ? (
              <button type="button" className={styles.btn} disabled={running} onClick={() => setView('list')}>
                Conversations
              </button>
            ) : (
              <button type="button" className={styles.btn} onClick={() => setView('chat')}>
                Back to chat
              </button>
            )}
            <button type="button" className={styles.btn} disabled={running} onClick={startNew}>
              New conversation
            </button>
          </div>
          {view === 'list' ? (
            <ConversationList items={sorted} loading={conversations.isPending} failed={conversations.isError} currentId={currentId} onOpen={open} onDelete={setToDelete} />
          ) : (
            <>
              {label && <p className={styles.looking}>{`Looking at: ${label}`}</p>}
              <div ref={scrollRef} className={styles.messages} aria-live="polite">
                {live ? (
                  <>
                    {live.before.map((message) => <Message key={message.id} message={message} />)}
                    <LiveMessages turn={live} />
                    {notices.map((notice) => (
                      <p key={notice} className={styles.notice}>
                        {notice}
                      </p>
                    ))}
                  </>
                ) : !ready || (currentId !== null && conversation.isPending) ? (
                  <Skeleton lines={3} label="Loading conversation" />
                ) : (
                  <>
                    {loadError && (
                      <p className={styles.problem} role="alert">
                        {loadError}
                      </p>
                    )}
                    {currentId === null && !loadError && <p className={styles.empty}>Ask a question about what you are looking at.</p>}
                    {conversation.data?.messages.map((message) => <Message key={message.id} message={message} />)}
                    {notices.map((notice) => (
                      <p key={notice} className={styles.notice}>
                        {notice}
                      </p>
                    ))}
                    {problem && (
                      <p className={styles.problem} role="alert">
                        {problem}
                      </p>
                    )}
                  </>
                )}
              </div>
            </>
          )}
        </div>
      </Drawer>
      {toDelete && (
        <ConfirmDialog
          title="Delete conversation"
          sub={toDelete.title || 'Untitled conversation'}
          confirmLabel="Delete"
          danger
          confirmDisabled={remove.isPending}
          onConfirm={confirmDelete}
          onCancel={() => setToDelete(null)}
        >
          <p>The conversation and its messages are removed for good.</p>
        </ConfirmDialog>
      )}
    </>
  )
}

function ToolChips({ tools }: { tools: ToolRun[] }) {
  if (tools.length === 0) return null
  return (
    <ul className={styles.tools} aria-label="Tools used">
      {tools.map((tool, index) => (
        <li key={index} className={`${styles.tool} ${tool.state === 'failed' ? styles.toolFailed : ''}`} data-state={tool.state}>
          {tool.name}
        </li>
      ))}
    </ul>
  )
}

function Message({ message }: { message: AgentMessageDTO }) {
  if (message.role === 'user') return <div className={styles.userMsg}>{message.text}</div>
  return (
    <div className={styles.agentMsg}>
      <ToolChips tools={message.tools.map((name) => ({ name, state: 'ok' as const }))} />
      <AnswerText text={message.text} />
      {message.interrupted && <p className={styles.notice}>Stopped before finishing.</p>}
    </div>
  )
}

function LiveMessages({ turn }: { turn: LiveTurn }) {
  return (
    <>
      <div className={styles.userMsg}>{turn.question}</div>
      <div className={styles.agentMsg}>
        <ToolChips tools={turn.tools} />
        {turn.text ? <AnswerText text={turn.text} /> : <p className={styles.notice}>Working on it…</p>}
      </div>
    </>
  )
}

function ConversationList({
  items,
  loading,
  failed,
  currentId,
  onOpen,
  onDelete,
}: {
  items: AgentConversationSummaryDTO[]
  loading: boolean
  failed: boolean
  currentId: number | null
  onOpen: (id: number) => void
  onDelete: (item: AgentConversationSummaryDTO) => void
}) {
  if (failed) return <p className={styles.problem} role="alert">{LOAD_FAILED}</p>
  if (loading) return <Skeleton lines={3} label="Loading conversations" />
  if (items.length === 0) return <p className={styles.empty}>No conversations yet.</p>
  return (
    <ul className={styles.convList}>
      {items.map((item) => {
        const title = item.title || 'Untitled conversation'
        return (
          <li key={item.id} className={styles.convRow}>
            <button type="button" className={styles.convOpen} aria-label={`Open ${title}`} aria-current={item.id === currentId ? 'true' : undefined} onClick={() => onOpen(item.id)}>
              <span className={styles.convTitle}>{title}</span>
              <span className={styles.convWhen}>
                <Ago ts={item.updated_at} />
              </span>
            </button>
            <button type="button" className={styles.btn} aria-label={`Delete ${title}`} onClick={() => onDelete(item)}>
              Delete
            </button>
          </li>
        )
      })}
    </ul>
  )
}
