import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { InvestigationDetailDTO } from '../../api/investigations'
import { INVESTIGATION_DONE } from '../../test/investigationFixtures'
import { InvestigationView } from './InvestigationView'

const show = (overrides: Partial<InvestigationDetailDTO> = {}) =>
  render(
    <MemoryRouter>
      <InvestigationView investigation={{ ...INVESTIGATION_DONE, ...overrides }} />
    </MemoryRouter>,
  )

describe('InvestigationView', () => {
  it('renders summary and evidence as text', () => {
    show()

    expect(screen.getByText('Diagnosis ready')).toBeInTheDocument()
    expect(screen.getByText(/The tasks fail to start because the image tag does not exist\./)).toBeInTheDocument()
    expect(screen.getByText('payments: 0 of 2 tasks running')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'payments' })).toHaveAttribute('href', `/services/${INVESTIGATION_DONE.subject.service_id}/timeline`)
    const first = screen.getByText('CannotPullContainerError: manifest unknown')
    expect(first.tagName).toBe('PRE')
    expect(screen.getByText('Start failures began after the deploy.')).toBeInTheDocument()
    expect(screen.getByText('get_service_events')).toBeInTheDocument()
    expect(screen.getByText('View')).toBeInTheDocument()
    expect(screen.getByText('Logs')).toBeInTheDocument()
  })

  it('a link or image in the summary is shown as characters', () => {
    const summary = 'See [the fix](https://evil.example/x) ![pic](https://evil.example/p.png) <a href="https://evil.example">go</a> **bold**'
    const { container } = show({
      summary,
      evidence: [{ ...INVESTIGATION_DONE.evidence[0], note: '<img src=x onerror=alert(1)> [n](https://evil.example)', excerpt: '[x](https://evil.example) <b>hi</b>' }],
    })

    expect(container.querySelector('[aria-label="Summary"] p')?.textContent).toContain(summary)
    expect(container.querySelector('img, strong, b')).toBeNull()
    expect(container.querySelector('a[href*="evil"]')).toBeNull()
    expect(screen.getByText('[x](https://evil.example) <b>hi</b>').tagName).toBe('PRE')
    expect(screen.getByText('<img src=x onerror=alert(1)> [n](https://evil.example)')).toBeInTheDocument()
  })

  it('shows the hidden evidence line', () => {
    show({ evidence: [INVESTIGATION_DONE.evidence[0]], evidence_hidden: 7 })

    expect(screen.getByText('Some evidence is not shown because you do not have permission to read it.')).toBeInTheDocument()
    expect(screen.queryByText(/\b7\b/)).not.toBeInTheDocument()
  })

  it('shows no hidden line when nothing is hidden', () => {
    show()

    expect(screen.queryByText(/not shown/)).not.toBeInTheDocument()
  })

  it('shows the fixed sentence when replaced', () => {
    const fixed = 'The investigation finished. Open it in Harbormaster to read the findings.'
    show({ summary: fixed, summary_replaced: true })

    expect(screen.getByText(fixed)).toBeInTheDocument()
    expect(screen.queryByText(/replaced|removed|edited/i)).not.toBeInTheDocument()
  })

  it('lists group members', () => {
    show({
      subject: { ...INVESTIGATION_DONE.subject, kind: 'cluster', service_id: '', service: '' },
      members: [
        { service_id: 'a', service: 'payments', cluster: 'prod-main' },
        { service_id: 'b', service: 'ledger', cluster: 'prod-main' },
      ],
    })

    const list = screen.getByRole('list', { name: 'Services' })
    expect(within(list).getByText('payments')).toBeInTheDocument()
    expect(within(list).getByText('ledger')).toBeInTheDocument()
  })

  it('says why there is no diagnosis for a skipped investigation', () => {
    show({ state: 'skipped', reason: 'budget', summary: '', evidence: [] })

    expect(screen.getByText("Not investigated: this month's AI allowance is used up.")).toBeInTheDocument()
  })

  it('captions the summary as AI-written, but not the fixed sentence', () => {
    const caption = 'Written by AI from what its identity may read. Check it before acting.'
    const { unmount } = show()
    expect(screen.getByText(caption)).toBeInTheDocument()
    unmount()

    show({ summary: 'The investigation finished. Open it in Harbormaster to read the findings.', summary_replaced: true })
    expect(screen.queryByText(caption)).not.toBeInTheDocument()
  })

  it('renders nothing for an empty note or excerpt, and no excerpt for an unknown action', () => {
    const base = INVESTIGATION_DONE.evidence[0]
    const { container } = show({
      evidence: [
        { ...base, ordinal: 1, note: '', excerpt: 'only the excerpt' },
        { ...base, ordinal: 2, note: 'only the note', excerpt: '' },
        { ...base, ordinal: 3, action: 'delete', note: 'secret note', excerpt: 'secret excerpt' },
      ],
    })

    expect(container.querySelectorAll('pre')).toHaveLength(1)
    expect(screen.getByText('only the excerpt')).toBeInTheDocument()
    expect(screen.getByText('only the note')).toBeInTheDocument()
    expect(screen.queryByText('secret excerpt')).not.toBeInTheDocument()
    expect(screen.queryByText('secret note')).not.toBeInTheDocument()
    expect(container.querySelectorAll('p:empty')).toHaveLength(0)
  })
})
