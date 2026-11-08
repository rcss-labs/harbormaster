import { screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../../../test/msw'
import { renderWithProviders } from '../../../test/setup'
import {
  DIGEST_GATE_CRITICAL,
  DIGEST_QUEUED,
  SCAN_GATE_CRITICAL,
  SCAN_GATE_FAILED,
  SCAN_GATE_QUEUED,
  SCAN_STATUS_QUEUED,
  SCAN_STATUS_SCANNED,
} from '../../../test/securityFixtures'
import { ScanGatePanel } from './ScanGatePanel'

describe('ScanGatePanel', () => {
  it('shows counts and a findings link for a critical row, and the server message for a failed one', () => {
    renderWithProviders(<ScanGatePanel serviceId="svc" gate={{ ...SCAN_GATE_CRITICAL, images: [...SCAN_GATE_CRITICAL.images, ...SCAN_GATE_FAILED.images] }} onRowSettled={() => {}} />)
    expect(screen.getByText(/1 critical, 0 high, 0 medium/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open findings' })).toHaveAttribute('href', `/security?image=${DIGEST_GATE_CRITICAL}`)
    expect(screen.getByText(SCAN_GATE_FAILED.images[0].message)).toBeInTheDocument()
    expect(screen.getByText('A scan never blocks a deploy; critical findings pause it until you choose.')).toBeInTheDocument()
  })

  it('polls a pending row by digest and reports it once the scan has moved on', async () => {
    const reads: URLSearchParams[] = []
    server.use(
      http.get('/api/security/services/:serviceId/scans', ({ request }) => {
        reads.push(new URL(request.url).searchParams)
        return HttpResponse.json(reads.length === 1 ? SCAN_STATUS_QUEUED : SCAN_STATUS_SCANNED)
      }),
    )
    const onRowSettled = vi.fn()
    renderWithProviders(<ScanGatePanel serviceId="svc" gate={SCAN_GATE_QUEUED} onRowSettled={onRowSettled} />)
    expect(await screen.findByText('Queued')).toBeInTheDocument()
    expect(onRowSettled).not.toHaveBeenCalled()
    await waitFor(() => expect(onRowSettled).toHaveBeenCalledWith('payments-api'), { timeout: 8000 })
    expect(reads[0].get('digest')).toBe(DIGEST_QUEUED)
    expect(reads[0].has('tag')).toBe(false)
  })
})
