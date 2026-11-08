import { useCallback, useEffect, useRef, useState } from 'react'
import { isApiError } from '../../../api/change'
import type { ScanGateConflictBody } from '../../../api/deploy'
import type { ScanGate, ScanGateImage, ScanGateRequest, ScanGateState } from '../../../api/security'

/** Gate states that still move on the server; a row in one of them can be followed by its digest. */
export const PENDING_GATE_STATES: readonly ScanGateState[] = ['queued', 'scanning', 'refreshing_db']

export const isPendingGateRow = (row: ScanGateImage): boolean => PENDING_GATE_STATES.includes(row.state) && row.digest !== ''

/** The gate a failed write stopped on, or null when the failure is anything else. */
export function scanGateOf(failure: unknown): ScanGate | null {
  if (!isApiError(failure, 'scan_gate')) return null
  return (failure.body as Partial<ScanGateConflictBody> | undefined)?.scan_gate ?? null
}

/**
 * Wraps a write that the pre-deploy image scan gate can answer with a 409 `scan_gate`. `send` performs the write and handles
 * its own results and errors, but lets a `scan_gate` error propagate (see `scanGateOf`). The hook then keeps the request and the
 * latest gate: once every waiting row has settled it resends the same request (the server re-evaluates the gate each time),
 * and "Deploy anyway" resends it with an acknowledgement built from the rows of the latest 409 only.
 */
export function useGatedSubmit<Request extends { scan_gate?: ScanGateRequest }>(send: (request: Request) => Promise<void>) {
  const [gate, setGate] = useState<ScanGate | null>(null)
  const [settled, setSettled] = useState<ReadonlySet<string>>(new Set())
  const [busy, setBusy] = useState(false)
  // The request as the caller submitted it: what an automatic resubmission sends. A partial retry submits a request that already
  // carries its earlier acknowledgement; the server ignores an acknowledgement that no longer matches the latest rows.
  const original = useRef<Request | null>(null)
  const resumed = useRef<ScanGate | null>(null)
  // Set synchronously, unlike `busy`, so the automatic resubmission and a Deploy anyway click can never both send.
  const inFlight = useRef(false)
  // Bumped by cancel: an answer to a request sent before it must not bring a gate back, because Deploy anyway would have nothing to resend.
  const generation = useRef(0)

  const run = async (request: Request) => {
    if (inFlight.current) return
    inFlight.current = true
    const sentIn = generation.current
    setBusy(true)
    try {
      await send(request)
      setGate(null)
    } catch (failure) {
      const paused = scanGateOf(failure)
      if (!paused) {
        setGate(null)
        throw failure
      }
      if (generation.current === sentIn) {
        setGate(paused)
        setSettled(new Set())
      }
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }

  const submit = (request: Request) => {
    original.current = request
    return run(request)
  }

  const settleRow = useCallback((container: string) => {
    setSettled((current) => (current.has(container) ? current : new Set(current).add(container)))
  }, [])

  const deployAnyway = () => {
    if (!gate || !original.current) return
    const images = gate.images.filter((row) => row.state !== 'pass').map(({ container, ref }) => ({ container, ref }))
    void run({ ...original.current, scan_gate: { deploy_anyway: true, images } })
  }

  const cancel = useCallback(() => {
    generation.current += 1
    original.current = null
    setGate(null)
  }, [])

  // Once no row is waiting any more, the same request goes again. Each gate is resumed at most once, so a gate that comes back
  // paused for another reason waits for the user instead of looping.
  useEffect(() => {
    if (!gate || busy || inFlight.current || resumed.current === gate || !original.current) return
    const waiting = gate.images.filter(isPendingGateRow)
    if (waiting.length === 0 || !waiting.every((row) => settled.has(row.container))) return
    resumed.current = gate
    // `send` reports its own failures; nothing is left to surface from here.
    run(original.current).catch(() => {})
  })

  return { gate, busy, submit, settleRow, deployAnyway, cancel }
}
