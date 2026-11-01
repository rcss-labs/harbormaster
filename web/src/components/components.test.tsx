import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SlotDTO, Status, TagDTO } from '../api/types'
import { renderWithProviders } from '../test/setup'
import { AdminOnly } from './AdminOnly'
import { Ago } from './Ago'
import { Dialog } from './Dialog'
import { LockPanel } from './LockPanel'
import { Pill } from './Pill'
import { Slots } from './Slots'
import { statusKind, statusLabel } from './status'
import { Tabs } from './Tabs'
import { TagChip } from './TagChip'

const prodTag: TagDTO = { id: 3, name: 'Prod', color: 'red', inherited: false }

describe('shared components', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('Pill renders label and kind class', () => {
    render(<Pill kind="warn">Degraded</Pill>)
    const pill = screen.getByText('Degraded')
    expect(pill).toHaveClass('pill', 'warn')
  })

  it('TagChip marks inherited with dashed class and title', () => {
    render(<TagChip tag={{ ...prodTag, inherited: true }} inheritedFrom="payments" />)
    const chip = screen.getByText('Prod')
    expect(chip).toHaveClass('label', 'c-red', 'inh')
    expect(chip).toHaveAttribute('title', 'Inherited from cluster payments')
  })

  it('TagChip treats tag.inherited as inherited even without a cluster name', () => {
    render(<TagChip tag={{ ...prodTag, inherited: true }} />)
    expect(screen.getByText('Prod')).toHaveClass('inh')
  })

  it('Tabs renders ⊘ and title for locked tab', () => {
    renderWithProviders(
      <Tabs
        active="ops"
        hrefFor={(key) => `/x/${key}`}
        items={[
          { key: 'ops', label: 'Operations' },
          { key: 'logs', label: 'Logs', locked: { action: 'Logs', tag: 'Prod' } },
        ]}
      />,
    )
    const locked = screen.getByRole('link', { name: /Logs/ })
    expect(locked).toHaveTextContent('Logs ⊘')
    expect(locked).toHaveAttribute('title', 'You need Logs on Prod')
    expect(locked).toHaveAttribute('href', '/x/logs')
    expect(screen.getByRole('link', { name: 'Operations' })).toHaveClass('active')
  })

  it('Dialog closes on Escape and calls action', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    const onSave = vi.fn()
    render(
      <Dialog title="Edit" actions={[{ label: 'Save', kind: 'primary', onClick: onSave }]} onClose={onClose}>
        <input aria-label="name" />
      </Dialog>,
    )
    expect(screen.getByRole('dialog', { name: 'Edit' })).toBeInTheDocument()
    expect(screen.getByLabelText('name')).toHaveFocus()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledTimes(1)
    await user.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('Ago formats 30s/5min/3h/2d', () => {
    const now = 1_700_000_000_000
    const { rerender } = render(<Ago ts={now - 30_000} now={now} />)
    expect(screen.getByText('30s ago')).toBeInTheDocument()
    rerender(<Ago ts={now - 5 * 60_000} now={now} />)
    expect(screen.getByText('5 min ago')).toBeInTheDocument()
    rerender(<Ago ts={now - 3 * 3600_000} now={now} />)
    expect(screen.getByText('3 h ago')).toBeInTheDocument()
    rerender(<Ago ts={new Date(now - 2 * 86400_000).toISOString()} now={now} />)
    expect(screen.getByText('2 d ago')).toBeInTheDocument()
    expect(screen.getByText('2 d ago')).toHaveAttribute('title', new Date(now - 2 * 86400_000).toLocaleString())
  })

  it('Ago shows "no data yet" for the zero time the API sends before the first poll', () => {
    render(<Ago ts="0001-01-01T00:00:00Z" now={1_700_000_000_000} />)
    expect(screen.getByText('no data yet')).toBeInTheDocument()
  })

  it('Ago keeps its label live when now is omitted', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T12:00:00Z'))
    render(<Ago ts={Date.now()} />)
    expect(screen.getByText('0s ago')).toBeInTheDocument()
    act(() => {
      vi.advanceTimersByTime(2 * 60_000)
    })
    expect(screen.getByText('2 min ago')).toBeInTheDocument()
  })

  it('Slots draws desired-running dashed slots', () => {
    const slots: SlotDTO[] = [
      { task_id: 'a', short_id: 'a', private_ip: '10.0.0.1', health: 'HEALTHY', rolling_out: false },
      { task_id: 'b', short_id: 'b', private_ip: '10.0.0.2', health: 'UNHEALTHY', rolling_out: false },
      { task_id: 'c', short_id: 'c', private_ip: '10.0.0.3', health: 'HEALTHY', rolling_out: true },
    ]
    const { container } = render(<Slots slots={slots} desired={5} />)
    expect(container.querySelectorAll('.slot')).toHaveLength(5)
    expect(container.querySelectorAll('.slot.empty')).toHaveLength(2)
    expect(screen.getAllByTitle('missing task')).toHaveLength(2)
    // Read the inline style: jsdom 30's computed style no longer carries var() through the background shorthand.
    expect((container.querySelectorAll<HTMLElement>('.slot')[2]).style.background).toBe('var(--info)')
  })

  it('statusLabel covers every status', () => {
    const expected: Record<Status, string> = {
      steady: 'Steady',
      deploying: 'Deploying',
      degraded: 'Degraded',
      unhealthy_task: 'Unhealthy task',
      failing: 'Failing',
      scaled_to_zero: 'Scaled to 0',
    }
    for (const [status, label] of Object.entries(expected)) {
      expect(statusLabel(status as Status)).toBe(label)
    }
  })

  it('an empty status (operation without a service, or a gone service) renders as a neutral dash', () => {
    expect(statusLabel('')).toBe('–')
    expect(statusKind('')).toBe('mute')
  })

  it('LockPanel names the action and tag', () => {
    render(<LockPanel action="Logs" tag={prodTag} />)
    expect(screen.getByText('Logs')).toBeInTheDocument()
    expect(screen.getByText('Prod')).toHaveClass('label')
    expect(screen.getByText(/Ask a global admin to change your permissions\./)).toBeInTheDocument()
  })

  it('AdminOnly shows the per-area copy', () => {
    render(<AdminOnly area="audit" />)
    expect(screen.getByRole('heading', { name: 'No access' })).toBeInTheDocument()
    expect(screen.getByText('Only global admins can read the audit log.')).toBeInTheDocument()
  })
})
