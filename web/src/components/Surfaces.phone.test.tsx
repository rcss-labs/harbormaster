import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installPhoneMedia } from '../test/phone'
import { KpiStrip } from './KpiStrip'
import { Panel } from './Panel'
import { Seg } from './Seg'
import { ToastProvider, useToast } from './Toast'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('Seg on phones', () => {
  it('phone segments wrap and keep nowrap labels (class assertions)', () => {
    render(<Seg label="Level" options={[{ value: 'a', label: 'Everything' }, { value: 'b', label: 'Errors only' }]} value="a" onChange={() => {}} />)
    const group = screen.getByRole('group', { name: 'Level' })
    expect(group.className).toContain('phone')
    expect(group.className).not.toContain('fit')
    for (const button of screen.getAllByRole('button')) expect(button).toHaveAttribute('data-hm-oneline')
  })

  it('fit keeps the group at its content width', () => {
    render(<Seg label="Tail" fit options={[{ value: 'a', label: '100' }]} value="a" onChange={() => {}} />)
    expect(screen.getByRole('group', { name: 'Tail' }).className).toContain('fit')
  })
})

describe('KpiStrip on phones', () => {
  it('three columns on phones', () => {
    const items = ['One', 'Two', 'Three', 'Four'].map((label) => ({ label, value: 1 }))
    render(
      <MemoryRouter>
        <KpiStrip label="Totals" items={items} />
      </MemoryRouter>,
    )
    const strip = screen.getByRole('list', { name: 'Totals' })
    expect(strip.className).toContain('phone')
    expect(strip.style.gridTemplateColumns).toBe('repeat(3, minmax(0, 1fr))')
  })

  it('keeps the item count as columns on desktop', () => {
    restoreMedia()
    restoreMedia = installPhoneMedia(false)
    const items = ['One', 'Two', 'Three', 'Four'].map((label) => ({ label, value: 1 }))
    render(
      <MemoryRouter>
        <KpiStrip label="Totals" items={items} />
      </MemoryRouter>,
    )
    expect(screen.getByRole('list', { name: 'Totals' }).style.gridTemplateColumns).toBe('repeat(4, minmax(0, 1fr))')
  })
})

describe('Toast on phones', () => {
  function Fire() {
    const toast = useToast()
    return (
      <button type="button" onClick={() => toast.show('Saved')}>
        Fire
      </button>
    )
  }

  it('bottom inset class on phones', async () => {
    render(
      <ToastProvider>
        <Fire />
      </ToastProvider>,
    )
    await userEvent.click(screen.getByRole('button', { name: 'Fire' }))
    expect(screen.getByRole('status').className).toContain('phone')
    expect(screen.getByRole('status')).toHaveTextContent('Saved')
  })
})

describe('Panel on phones', () => {
  it('error with Retry at phone width', async () => {
    const onRetry = vi.fn()
    render(
      <Panel title="Metrics" error={{ message: 'CloudWatch is throttling requests.', onRetry }}>
        body
      </Panel>,
    )
    expect(screen.getByRole('region', { name: 'Metrics' })).toHaveTextContent('CloudWatch is throttling requests.')
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledOnce()
    expect(screen.queryByText('body')).not.toBeInTheDocument()
  })
})
