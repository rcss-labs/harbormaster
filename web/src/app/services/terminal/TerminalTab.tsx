import '@xterm/xterm/css/xterm.css'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useExecTargets } from '../../../api/exec'
import { useMe, useService, useServiceTasks } from '../../../api/hooks'
import type { TaskDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { EmptyState } from '../../../components/EmptyState'
import { LockPanel } from '../../../components/LockPanel'
import { useExecSocket, type ExecError, type ExecStatus } from './useExecSocket'
import { Panel } from '../../../components/Panel'
import { BottomSheet } from '../../../components/BottomSheet'
import { Icon } from '../../../components/Icon'
import { usePhone } from '../../../components/usePhone'
import { TerminalKeys } from './TerminalKeys'
import styles from './terminal.module.css'

const SYS = '\x1b[38;2;124;196;228m'
const RESET = '\x1b[0m'

/** A colour token's current value, so the xterm canvas follows the theme; `fallback` covers environments without the stylesheet. */
const cssColor = (name: string, fallback: string): string =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback

/** The danger colour token as an ANSI truecolor escape; falls back to the dark-theme value. */
function dangerEscape(): string {
  const raw = getComputedStyle(document.documentElement).getPropertyValue('--danger').trim()
  const hex = /^#([0-9a-f]{6})$/i.exec(raw)?.[1] ?? 'f87171'
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16))
  return `\x1b[38;2;${r};${g};${b}m`
}

export function TerminalTab({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId).data
  const tasks = useServiceTasks(serviceId)
  if (!service) return null
  if (!service.can.terminal) return <LockPanel action="Terminal" tag={service.tag} />
  if (!service.enable_execute_command) {
    return (
      <Banner kind="warn">
        <strong>ECS Exec is off for {service.name}.</strong> Turn on <code>enable_execute_command = true</code> on the
        service in Terraform and give the task role the <code>ssmmessages:*</code> permissions. New tasks then accept
        terminal sessions.
      </Banner>
    )
  }
  const running = (tasks.data?.items ?? []).filter((task) => task.status.toUpperCase() === 'RUNNING')
  if (running.length === 0) {
    if (!tasks.data) return null
    return <EmptyState>No running tasks to connect to.</EmptyState>
  }
  return <TerminalPanel running={running} />
}

export function TerminalPanel({ running }: { running: TaskDTO[] }) {
  const [params] = useSearchParams()
  const csrf = useMe().data?.csrf_token ?? ''
  const phone = usePhone()
  const [targetOpen, setTargetOpen] = useState(false)
  const [sessionOpen, setSessionOpen] = useState(false)
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<{ fit(): void } | null>(null)

  const [pickedTask, setPickedTask] = useState<string>()
  const [pickedContainer, setPickedContainer] = useState<string>()
  const [pickedCommand, setPickedCommand] = useState<string>()

  const wanted = params.get('task')
  const task =
    running.find((t) => t.id === pickedTask) ?? running.find((t) => t.id === wanted || t.short_id === wanted) ?? running[0]

  const sysLine = (text: string) => termRef.current?.write(`${SYS}${text}${RESET}\r\n`)
  const errorLine = (text: string) => termRef.current?.write(`${dangerEscape()}${text}${RESET}\r\n`)

  const socket = useExecSocket({
    onData: (bytes) => termRef.current?.write(bytes),
    onStatus: (status: ExecStatus) => {
      if (status.state === 'connecting') sysLine('Starting session…')
      else if (status.state === 'connected') sysLine(`Starting session with SessionId: ${status.session_id}`)
      else {
        termRef.current?.write(`${SYS}\r\n\r\nExiting session with sessionId: ${status.session_id ?? ''}.\r\n${RESET}`)
        if (status.reason === 'idle_timeout') sysLine('Disconnected after 20 minutes without input.')
        if (status.reason === 'max_duration') sysLine('Session reached the 4 hour limit.')
      }
    },
    onError: (error: ExecError) => {
      errorLine(error.message)
      if (error.code === 'session_limit') sysLine('Close another terminal to open a new one.')
    },
  })
  const { send, resize, disconnect } = socket
  const active = socket.state === 'connecting' || socket.state === 'connected'

  const targets = useExecTargets(task.id, socket.state === 'connected').data
  const container = targets?.containers.find((c) => c.name === pickedContainer) ?? targets?.containers.find((c) => c.exec_agent === 'RUNNING') ?? targets?.containers[0]
  const commands = targets?.commands ?? ['/bin/sh', '/bin/bash']
  const command = commands.find((c) => c === pickedCommand) ?? (commands.includes('/bin/sh') ? '/bin/sh' : commands[0])
  const canConnect = !active && container?.exec_agent === 'RUNNING' && csrf !== ''

  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    const term = new Terminal({
      fontFamily: getComputedStyle(document.documentElement).getPropertyValue('--mono').trim() || 'monospace',
      fontSize: 12.5,
      theme: { background: cssColor('--term', '#0d1217'), foreground: cssColor('--term-text', '#d7e3ec') },
      cursorBlink: true,
      scrollback: 5000,
      convertEol: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host)
    termRef.current = term
    // Fit, then (on phones) drop rows until the screen ends inside the host: the fit addon sizes rows by a cell height that
    // can differ from the rendered one while the mono font is still loading, which would paint the last rows below the host.
    const refit = () => {
      fit.fit()
      if (!phone) return
      const screen = host.querySelector<HTMLElement>('.xterm-screen')
      const xtermEl = host.querySelector<HTMLElement>('.xterm')
      if (!screen || !xtermEl || term.rows < 2) return
      const room = host.getBoundingClientRect().bottom - screen.getBoundingClientRect().top - parseFloat(getComputedStyle(xtermEl).paddingBottom || '0')
      const screenHeight = screen.getBoundingClientRect().height
      if (room > 0 && screenHeight > room + 0.5) term.resize(term.cols, Math.max(1, Math.floor(room / (screenHeight / term.rows))))
    }
    fitRef.current = { fit: refit }
    refit()
    term.write(`${SYS}Pick a task and a container, then Connect.${RESET}\r\n`)
    const dataSub = term.onData((data) => send(data))
    const resizeSub = term.onResize(({ cols, rows }) => resize(cols, rows))
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(refit)
    observer?.observe(host)
    // The page settles after the first paint; fit once more so the rows match the final host height.
    const settled = typeof requestAnimationFrame === 'function' ? requestAnimationFrame(refit) : 0
    // The host does not resize when the mono font arrives, so refit on font loads too.
    document.fonts?.addEventListener?.('loadingdone', refit)
    return () => {
      document.fonts?.removeEventListener?.('loadingdone', refit)
      if (settled) cancelAnimationFrame(settled)
      observer?.disconnect()
      dataSub.dispose()
      resizeSub.dispose()
      disconnect()
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [send, resize, disconnect, phone]) // the host element is a different node on phones, so crossing the breakpoint starts a fresh terminal

  // While a phone session is connected the keys row replaces the tab bar (data-hm-keys) and the page ends above it.
  const keysShown = phone && socket.state === 'connected'
  useEffect(() => {
    if (!keysShown) return
    const root = document.documentElement
    root.setAttribute('data-hm-keys', '')
    return () => root.removeAttribute('data-hm-keys')
  }, [keysShown])

  // The on-screen keyboard shrinks the visual viewport; the host follows the layout, and xterm refits to it.
  useEffect(() => {
    const viewport = phone ? window.visualViewport : null
    if (!viewport) return
    const refit = () => fitRef.current?.fit()
    viewport.addEventListener('resize', refit)
    return () => viewport.removeEventListener('resize', refit)
  }, [phone])

  const connect = () => {
    if (!container) return
    fitRef.current?.fit()
    const term = termRef.current
    socket.connect({ taskId: task.id, container: container.name, command, cols: term?.cols ?? 80, rows: term?.rows ?? 24, csrf })
  }

  const taskSelect = (
    <select value={task.id} disabled={active} onChange={(e) => { setPickedTask(e.target.value); setPickedContainer(undefined) }}>
      {running.map((t) => (
        <option key={t.id} value={t.id}>{`${t.short_id.slice(0, 12)} · ${t.private_ip} · ${t.az}`}</option>
      ))}
    </select>
  )
  const containerSelect = (
    <select value={container?.name ?? ''} disabled={active} onChange={(e) => setPickedContainer(e.target.value)}>
      {(targets?.containers ?? []).map((c) => (
        <option key={c.name} value={c.name} disabled={c.exec_agent !== 'RUNNING'}>
          {c.exec_agent === 'RUNNING' ? c.name : `${c.name} · agent ${c.exec_agent.toLowerCase()}`}
        </option>
      ))}
    </select>
  )
  const commandSelect = (
    <select value={command} disabled={active} onChange={(e) => setPickedCommand(e.target.value)}>
      {commands.map((c) => (
        <option key={c} value={c}>{c}</option>
      ))}
    </select>
  )
  const connectionButton =
    socket.state === 'connected' ? (
      <button type="button" className={`${styles.btn} ${styles.danger}`} onClick={disconnect}>Disconnect</button>
    ) : (
      <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={!canConnect} onClick={connect}>Connect</button>
    )
  const host = (
    <div className={styles.termWrap}>
      <div className={styles.term} ref={hostRef} data-testid="terminal-host" />
    </div>
  )
  const sessionFacts = (
    <>
      <dl className={styles.session}>
        <dt>Via</dt>
        <dd>ECS Exec</dd>
        <dt>Idle timeout</dt>
        <dd>20 minutes</dd>
        <dt>Max duration</dt>
        <dd>4 hours</dd>
      </dl>
      <p className={styles.sessionNote}>
        Keystrokes and output are not recorded. The operations log records who opened the session, the task, container and command.
      </p>
    </>
  )

  if (phone) {
    const targetLabel = `${task.short_id.slice(0, 12)} · ${container?.name ?? '…'} · ${command}`
    return (
      <div className={`${styles.page} ${styles.pagePhone}`}>
        <section className={`${styles.root} hm-fill-col`} aria-label="Terminal">
          <div className={styles.toolbar}>
            <button
              type="button"
              className={styles.targetBtn}
              aria-label={`Target: ${targetLabel}. Change task, container or command`}
              aria-haspopup="dialog"
              disabled={active}
              onClick={() => setTargetOpen(true)}
            >
              <Icon name="term" size={16} />
              <span className={styles.targetText}>{targetLabel}</span>
              <Icon name="chevd" size={16} />
            </button>
            {connectionButton}
            <button type="button" className={styles.infoBtn} aria-label="Session details" aria-haspopup="dialog" onClick={() => setSessionOpen(true)}>
              <Icon name="info" size={18} />
            </button>
          </div>
          {host}
        </section>
        {keysShown && <TerminalKeys send={send} />}
        <BottomSheet
          open={targetOpen}
          onClose={() => setTargetOpen(false)}
          label="Terminal target"
          title="Task, container and command"
          footer={
            <>
              <button type="button" className={styles.sheetBtn} onClick={() => setTargetOpen(false)}>Cancel</button>
              <button
                type="button"
                className={`${styles.sheetBtn} ${styles.sheetPrimary}`}
                disabled={!canConnect}
                onClick={() => {
                  setTargetOpen(false)
                  connect()
                }}
              >
                Connect
              </button>
            </>
          }
        >
          <div className={styles.sheetFields}>
            <label className={styles.sheetField}><span>Task</span>{taskSelect}</label>
            <label className={styles.sheetField}><span>Container</span>{containerSelect}</label>
            <label className={styles.sheetField}><span>Command</span>{commandSelect}</label>
          </div>
        </BottomSheet>
        <BottomSheet open={sessionOpen} onClose={() => setSessionOpen(false)} label="Session" title="Session" sub={socket.sessionId ? `Session ID ${socket.sessionId}` : undefined}>
          {sessionFacts}
        </BottomSheet>
      </div>
    )
  }

  return (
    <div className={`hm-fill hm-with-side ${styles.page}`}>
      <section className={`${styles.root} hm-fill-col`} aria-label="Terminal">
        <div className={styles.toolbar}>
          <label className={styles.field}>
            <span>Task</span>
            {taskSelect}
          </label>
          <label className={styles.field}>
            <span>Container</span>
            {containerSelect}
          </label>
          <label className={styles.field}>
            <span>Command</span>
            {commandSelect}
          </label>
          {connectionButton}
        </div>
        {host}
      </section>
      <div className={styles.side}>
        <Panel title="Session">{sessionFacts}</Panel>
      </div>
    </div>
  )
}
