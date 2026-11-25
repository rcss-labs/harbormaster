import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ResultBody } from './ResultBody'

const EVIDENCE = [
  { ordinal: 1, tool: 'get_service_events', action: 'view', service_id: 's1', service: 'payments', note: 'Start failures began.', excerpt: 'manifest unknown' },
]

const show = (overrides: Partial<Parameters<typeof ResultBody>[0]> = {}) =>
  render(<ResultBody summary="All fine." summaryReplaced={false} evidence={EVIDENCE} evidenceHidden={0} {...overrides} />)

describe('ResultBody', () => {
  it('renders summary and evidence as text', () => {
    show()

    expect(screen.getByText('All fine.')).toBeInTheDocument()
    expect(screen.getByText('Written by AI from what its identity may read. Check it before acting.')).toBeInTheDocument()
    expect(screen.getByText('manifest unknown').tagName).toBe('PRE')
    expect(screen.getByText('get_service_events')).toBeInTheDocument()
    expect(screen.getByText('View')).toBeInTheDocument()
    expect(screen.getByText('Start failures began.')).toBeInTheDocument()
  })

  it('a link or image in the summary is shown as characters', () => {
    const summary = 'See [fix](https://evil.example/x) ![pic](https://evil.example/p.png) <a href="https://evil.example">go</a> **bold**'
    const { container } = show({ summary, evidence: [{ ...EVIDENCE[0], note: '<img src=x onerror=alert(1)>', excerpt: '[x](https://evil.example)' }] })

    expect(container.querySelector('[aria-label="Summary"] p')?.textContent).toContain(summary)
    expect(container.querySelector('img, strong, b, a')).toBeNull()
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
  })

  it('shows the hidden evidence line', () => {
    show({ evidence: [], evidenceHidden: 2 })

    expect(screen.getByText('Some evidence is not shown because you do not have permission to read it.')).toBeInTheDocument()
  })

  it('leaves the caption off a replaced summary and shows nothing for an empty one', () => {
    const { rerender } = show({ summary: 'The review finished. Open it in Harbormaster to read the findings.', summaryReplaced: true, evidence: [] })
    expect(screen.getByText('The review finished. Open it in Harbormaster to read the findings.')).toBeInTheDocument()
    expect(screen.queryByText(/Written by AI/)).not.toBeInTheDocument()

    rerender(<ResultBody summary="" summaryReplaced={false} evidence={[]} evidenceHidden={0} />)
    expect(screen.queryByLabelText('Summary')).not.toBeInTheDocument()
  })
})
