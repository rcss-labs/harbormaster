import { useCallback, useEffect, useRef, useState } from 'react'
import { execSocketUrl } from '../../../api/exec'

export type ExecStatus = { state: 'connecting' | 'connected' | 'closed'; session_id?: string; reason?: string; duration_ms?: number }
export type ExecSocketState = 'idle' | 'connecting' | 'connected' | 'closed' | 'error'
export type ExecError = { code: string; message: string }

export interface ConnectParams {
  taskId: string
  container: string
  command: string
  cols: number
  rows: number
  csrf: string
}

interface Options {
  onData: (bytes: Uint8Array) => void
  onStatus: (status: ExecStatus) => void
  /** Called with the server's error message (or the synthesized close error). */
  onError?: (error: ExecError) => void
}

/** Owns one exec WebSocket: start frame, status/error frames, binary output and input. */
export function useExecSocket(options: Options) {
  const [state, setState] = useState<ExecSocketState>('idle')
  const [sessionId, setSessionId] = useState<string>()
  const [error, setError] = useState<ExecError>()
  const socketRef = useRef<WebSocket | null>(null)
  const sessionIdRef = useRef<string | undefined>(undefined)
  const optionsRef = useRef(options)
  useEffect(() => {
    optionsRef.current = options
  })

  const disconnect = useCallback(() => {
    const socket = socketRef.current
    if (!socket) return
    socketRef.current = null
    socket.close(1000)
    setState('closed')
    optionsRef.current.onStatus({ state: 'closed', session_id: sessionIdRef.current })
  }, [])

  const connect = useCallback((params: ConnectParams) => {
    socketRef.current?.close(1000)
    setError(undefined)
    setSessionId(undefined)
    sessionIdRef.current = undefined
    setState('connecting')
    const socket = new WebSocket(execSocketUrl(params.taskId, params.container, params.command))
    socket.binaryType = 'arraybuffer'
    socketRef.current = socket
    let sawError = false
    let sawClosed = false

    socket.addEventListener('open', () => {
      socket.send(JSON.stringify({ type: 'start', csrf: params.csrf, cols: params.cols, rows: params.rows }))
    })
    socket.addEventListener('message', (event: MessageEvent) => {
      if (socketRef.current !== socket) return
      if (typeof event.data !== 'string') {
        optionsRef.current.onData(new Uint8Array(event.data as ArrayBuffer))
        return
      }
      let frame: { type?: string } & Partial<ExecStatus> & Partial<ExecError>
      try {
        frame = JSON.parse(event.data)
      } catch {
        return
      }
      if (frame.type === 'status' && frame.state) {
        const status: ExecStatus = {
          state: frame.state,
          session_id: frame.session_id,
          reason: frame.reason,
          duration_ms: frame.duration_ms,
        }
        if (frame.session_id) {
          sessionIdRef.current = frame.session_id
          setSessionId(frame.session_id)
        }
        if (frame.state === 'closed') sawClosed = true
        setState(frame.state)
        optionsRef.current.onStatus(status)
      } else if (frame.type === 'error') {
        sawError = true
        const failure = { code: frame.code ?? 'error', message: frame.message ?? 'The terminal session failed.' }
        setError(failure)
        setState('error')
        optionsRef.current.onError?.(failure)
      }
    })
    socket.addEventListener('close', (event: CloseEvent) => {
      if (socketRef.current !== socket) return
      socketRef.current = null
      if (sawError) return
      // A closed status frame already explained the close (idle timeout, max duration), so no error.
      if (event.code !== 1000 && !sawClosed) {
        const failure = { code: `closed_${event.code}`, message: `Connection closed (${event.code}).` }
        setError(failure)
        setState('error')
        optionsRef.current.onError?.(failure)
        return
      }
      if (!sawClosed) optionsRef.current.onStatus({ state: 'closed', session_id: sessionIdRef.current })
      setState('closed')
    })
  }, [])

  const send = useCallback((data: Uint8Array<ArrayBuffer> | string) => {
    const socket = socketRef.current
    if (!socket || socket.readyState !== WebSocket.OPEN) return
    socket.send(typeof data === 'string' ? new TextEncoder().encode(data) : data)
  }, [])

  const resize = useCallback((cols: number, rows: number) => {
    const socket = socketRef.current
    if (!socket || socket.readyState !== WebSocket.OPEN) return
    socket.send(JSON.stringify({ type: 'resize', cols, rows }))
  }, [])

  useEffect(
    () => () => {
      const socket = socketRef.current
      socketRef.current = null
      socket?.close(1000)
    },
    [],
  )

  return { state, sessionId, error, connect, disconnect, send, resize }
}
