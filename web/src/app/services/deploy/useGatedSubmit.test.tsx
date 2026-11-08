import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../api/client'
import type { ScanGate, ScanGateRequest } from '../../../api/security'
import { SCAN_GATE_CRITICAL, SCAN_GATE_QUEUED } from '../../../test/securityFixtures'
import { useGatedSubmit } from './useGatedSubmit'

const gateError = (gate: ScanGate) => new ApiError('scan_gate', gate.message, 409, { scan_gate: gate })
const request: { base_revision: number; scan_gate?: ScanGateRequest } = { base_revision: 1 }

/** A send that answers each call with the next prepared outcome, once its resolver is called. */
function controlledSend() {
  const calls: { request: unknown; settle(outcome: 'ok' | ScanGate): void }[] = []
  const send = vi.fn(
    (sent: typeof request) =>
      new Promise<void>((resolve, reject) => {
        calls.push({ request: sent, settle: (outcome) => (outcome === 'ok' ? resolve() : reject(gateError(outcome))) })
      }),
  )
  return { send, calls }
}

async function pausedOn(gate: ScanGate) {
  const control = controlledSend()
  const hook = renderHook(() => useGatedSubmit(control.send))
  await act(async () => {
    void hook.result.current.submit(request)
    control.calls[0].settle(gate)
  })
  expect(hook.result.current.gate).toEqual(gate)
  return { ...control, hook }
}

describe('useGatedSubmit', () => {
  it('is single-flight: Deploy anyway right after the scan cleared adds no second request', async () => {
    const { send, hook } = await pausedOn(SCAN_GATE_QUEUED)
    // The cleared scan makes the hook resubmit; Deploy anyway is clicked before that request has answered.
    act(() => hook.result.current.settleRow(SCAN_GATE_QUEUED.images[0].container))
    act(() => hook.result.current.deployAnyway())
    await act(async () => {})
    expect(send).toHaveBeenCalledTimes(2)
  })

  it('a late 409 after cancel does not bring the panel back', async () => {
    const { calls, hook } = await pausedOn(SCAN_GATE_QUEUED)
    act(() => hook.result.current.settleRow(SCAN_GATE_QUEUED.images[0].container))
    await act(async () => {})
    expect(calls).toHaveLength(2)
    act(() => hook.result.current.cancel())
    await act(async () => calls[1].settle(SCAN_GATE_CRITICAL))
    expect(hook.result.current.gate).toBeNull()
  })

  it('a repeated 409 with the same container and digest still resumes once its rows clear', async () => {
    const { send, calls, hook } = await pausedOn(SCAN_GATE_QUEUED)
    const container = SCAN_GATE_QUEUED.images[0].container
    act(() => hook.result.current.settleRow(container))
    await act(async () => calls[1].settle({ ...SCAN_GATE_QUEUED, images: [{ ...SCAN_GATE_QUEUED.images[0] }] }))
    act(() => hook.result.current.settleRow(container))
    await act(async () => {})
    expect(send).toHaveBeenCalledTimes(3)
  })
})
