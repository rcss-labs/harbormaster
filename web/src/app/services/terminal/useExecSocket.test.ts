import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { MockWebSocket } from '../../../test/mockWebSocket'
import { useExecSocket } from './useExecSocket'

const params = { taskId: 'T1', container: 'payments-api', command: '/bin/sh', cols: 120, rows: 32, csrf: 'c1' }

function setup() {
  const onData = vi.fn()
  const onStatus = vi.fn()
  const hook = renderHook(() => useExecSocket({ onData, onStatus }))
  act(() => hook.result.current.connect(params))
  return { ...hook, onData, onStatus, socket: MockWebSocket.last }
}

describe('useExecSocket', () => {
  beforeEach(() => MockWebSocket.install())

  it('connect opens the exec URL and sends the start frame first', async () => {
    const { socket } = setup()
    await waitFor(() => expect(socket.sent).toHaveLength(1))
    expect(new URL(socket.url).pathname + new URL(socket.url).search).toBe('/api/tasks/T1/exec?container=payments-api&command=%2Fbin%2Fsh')
    expect(socket.binaryType).toBe('arraybuffer')
    expect(JSON.parse(socket.sent[0] as string)).toEqual({ type: 'start', csrf: 'c1', cols: 120, rows: 32 })
  })

  it('status frames drive state and onStatus', () => {
    const { result, socket, onStatus } = setup()
    act(() => socket.serverText({ type: 'status', state: 'connecting' }))
    expect(result.current.state).toBe('connecting')
    act(() => socket.serverText({ type: 'status', state: 'connected', session_id: 's-1' }))
    expect(result.current.state).toBe('connected')
    expect(result.current.sessionId).toBe('s-1')
    expect(onStatus).toHaveBeenLastCalledWith(expect.objectContaining({ state: 'connected', session_id: 's-1' }))
  })

  it('binary frames reach onData as Uint8Array', () => {
    const { socket, onData } = setup()
    act(() => socket.serverBinary([104, 105]))
    const bytes = onData.mock.calls[0][0] as Uint8Array
    expect(bytes).toBeInstanceOf(Uint8Array)
    expect(Array.from(bytes)).toEqual([104, 105])
  })

  it('send writes binary, resize writes a text frame', async () => {
    const { result, socket } = setup()
    await waitFor(() => expect(socket.readyState).toBe(1))
    act(() => {
      result.current.send('ls\r')
      result.current.resize(100, 40)
    })
    expect(Array.from(socket.sent[1] as Uint8Array)).toEqual([108, 115, 13])
    expect(socket.sent[2]).toBe('{"type":"resize","cols":100,"rows":40}')
  })

  it('error frame then close keeps the server message', () => {
    const { result, socket } = setup()
    act(() => {
      socket.serverText({ type: 'error', code: 'exec_disabled', message: 'ECS Exec is off for payments-api.' })
      socket.serverClose(4409)
    })
    expect(result.current.state).toBe('error')
    expect(result.current.error).toEqual({ code: 'exec_disabled', message: 'ECS Exec is off for payments-api.' })
  })

  it('abnormal close without error frame yields closed_<code>', () => {
    const { result, socket } = setup()
    act(() => socket.serverClose(1006))
    expect(result.current.error).toEqual({ code: 'closed_1006', message: 'Connection closed (1006).' })
  })

  it('disconnect closes with 1000 and state closed', () => {
    const { result, socket } = setup()
    act(() => result.current.disconnect())
    expect(socket.closedWith).toBe(1000)
    expect(result.current.state).toBe('closed')
  })

  it('a closed status followed by close 4408 stays closed without an error', () => {
    const { result, socket, onStatus } = setup()
    act(() => {
      socket.serverText({ type: 'status', state: 'connected', session_id: 's-1' })
      socket.serverText({ type: 'status', state: 'closed', session_id: 's-1', reason: 'idle_timeout' })
      socket.serverClose(4408)
    })
    expect(result.current.state).toBe('closed')
    expect(result.current.error).toBeUndefined()
    expect(onStatus.mock.calls.filter(([s]) => s.state === 'closed')).toHaveLength(1)
  })

  it('a clean close without a status frame reports the known session id', () => {
    const { socket, onStatus } = setup()
    act(() => {
      socket.serverText({ type: 'status', state: 'connected', session_id: 's-1' })
      socket.serverClose(1000)
    })
    expect(onStatus).toHaveBeenLastCalledWith({ state: 'closed', session_id: 's-1' })
  })
})
