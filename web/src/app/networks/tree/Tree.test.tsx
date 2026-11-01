import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { describe, expect, it } from 'vitest'
import { Tree } from './Tree'
import { buildTree, KEYS, SERVICE_KEYS } from './treeFixture'

function Location() {
  return <output data-testid="location">{useLocation().pathname}</output>
}

function renderTree(closed: ReadonlySet<string> = new Set(), onToggle: (key: string) => void = () => {}) {
  return render(
    <MemoryRouter initialEntries={['/vpc']}>
      <Routes>
        <Route path="/vpc" element={<Tree root={buildTree()} closed={closed} onToggle={onToggle} ariaLabel="Network tree" />} />
        <Route path="*" element={<Location />} />
      </Routes>
    </MemoryRouter>,
  )
}

const item = (container: HTMLElement, key: string) => container.querySelector<HTMLElement>(`[data-key="${key}"]`)!

describe('Tree', () => {
  it('renders ARIA tree roles and levels', () => {
    const { container } = renderTree()
    expect(screen.getByRole('tree', { name: 'Network tree' })).toBeInTheDocument()
    expect(item(container, KEYS.vpc)).toHaveAttribute('aria-level', '1')
    expect(item(container, KEYS.vpc)).toHaveAttribute('aria-expanded', 'true')
    expect(item(container, KEYS.subnetA)).toHaveAttribute('aria-level', '3')
    expect(item(container, KEYS.subnetA)).toHaveAttribute('aria-expanded', 'true')
    expect(item(container, KEYS.taskHealthy)).not.toHaveAttribute('aria-expanded')
    expect(item(container, KEYS.vpc)).toHaveAttribute('tabindex', '0')
    expect(item(container, KEYS.azA)).toHaveAttribute('tabindex', '-1')
  })

  it('shows "No running tasks" under an empty subnet', () => {
    renderTree()
    const none = screen.getByText('No running tasks').closest('li')
    expect(none).toHaveAttribute('role', 'none')
  })

  it('renders IP usage text and bar width', () => {
    const { container } = renderTree()
    expect(screen.getByText('27 of 251 IPs used')).toBeInTheDocument()
    const bar = (key: string) => item(container, key).querySelector<HTMLElement>('[class*="ipbar"] > span')!
    expect(bar(KEYS.subnetA).style.width).toBe('10.76%')
    expect(bar(KEYS.subnetEmpty).style.width).toBe('2%')
  })

  it('toggle hides children', async () => {
    const user = userEvent.setup()
    const toggled: string[] = []
    const { rerender, container } = renderTree(new Set(), (key) => toggled.push(key))
    const own = within(item(container, KEYS.payments)).getAllByRole('button', { name: 'Collapse' })[0]
    expect(own).toHaveTextContent('−')
    await user.click(own)
    expect(toggled).toEqual([KEYS.payments])
    rerender(
      <MemoryRouter initialEntries={['/vpc']}>
        <Routes>
          <Route path="/vpc" element={<Tree root={buildTree()} closed={new Set([KEYS.payments])} onToggle={() => {}} ariaLabel="Network tree" />} />
        </Routes>
      </MemoryRouter>,
    )
    expect(container.querySelector(`[data-key="${KEYS.taskHealthy}"]`)).toBeNull()
    expect(within(item(container, KEYS.payments)).getByRole('button', { name: 'Expand' })).toHaveTextContent('+')
  })

  it('collapsed set hides groups but keeps nodes', () => {
    const { container } = renderTree(new Set(SERVICE_KEYS))
    expect(container.querySelectorAll('[data-key^="task:"]')).toHaveLength(0)
    expect(item(container, KEYS.payments)).toHaveAttribute('aria-expanded', 'false')
    expect(item(container, KEYS.orders)).toBeInTheDocument()
    expect(item(container, KEYS.standalone)).toBeInTheDocument()
  })

  it('marks unhealthy task', () => {
    const { container } = renderTree()
    expect(item(container, KEYS.taskUnhealthy).querySelector('[class*="unhealthy"]')).not.toBeNull()
    expect(item(container, KEYS.taskHealthy).querySelector('[class*="unhealthy"]')).toBeNull()
  })

  it('standalone node lists tasks with group', () => {
    renderTree()
    expect(screen.getByText('Standalone tasks')).toBeInTheDocument()
    expect(screen.getByText('family:report-backfill')).toBeInTheDocument()
  })

  it('renders no tag chip when the cluster tag is unknown', () => {
    const { container } = renderTree()
    expect(within(item(container, KEYS.clusterA)).getByText('Prod')).toBeInTheDocument()
    expect(within(item(container, KEYS.clusterCi).querySelector('.node, [class*="node"]') as HTMLElement).queryByText('Prod')).toBeNull()
  })

  it('keyboard: ArrowDown/ArrowUp move focus, ArrowLeft collapses then moves to parent, ArrowRight expands then enters, Home/End', async () => {
    const user = userEvent.setup()
    // Stateful wrapper so collapse/expand work.
    const { useState } = await import('react')
    function Harness() {
      const [closed, setClosed] = useState<ReadonlySet<string>>(new Set())
      const onToggle = (key: string) =>
        setClosed((prev) => {
          const next = new Set(prev)
          if (!next.delete(key)) next.add(key)
          return next
        })
      return <Tree root={buildTree()} closed={closed} onToggle={onToggle} ariaLabel="Network tree" />
    }
    const { container } = render(
      <MemoryRouter>
        <Harness />
      </MemoryRouter>,
    )
    await user.tab()
    expect(item(container, KEYS.vpc)).toHaveFocus()
    await user.keyboard('{ArrowDown}')
    expect(item(container, KEYS.azA)).toHaveFocus()
    expect(item(container, KEYS.azA)).toHaveAttribute('tabindex', '0')
    expect(item(container, KEYS.vpc)).toHaveAttribute('tabindex', '-1')
    await user.keyboard('{ArrowDown}')
    expect(item(container, KEYS.subnetA)).toHaveFocus()
    await user.keyboard('{ArrowUp}')
    expect(item(container, KEYS.azA)).toHaveFocus()
    // open node: ArrowLeft collapses, second ArrowLeft goes to the parent
    await user.keyboard('{ArrowLeft}')
    expect(item(container, KEYS.azA)).toHaveAttribute('aria-expanded', 'false')
    expect(item(container, KEYS.azA)).toHaveFocus()
    await user.keyboard('{ArrowLeft}')
    expect(item(container, KEYS.vpc)).toHaveFocus()
    // ArrowRight on a closed node expands; on an open node enters first child
    await user.keyboard('{ArrowDown}{ArrowRight}')
    expect(item(container, KEYS.azA)).toHaveAttribute('aria-expanded', 'true')
    expect(item(container, KEYS.azA)).toHaveFocus()
    await user.keyboard('{ArrowRight}')
    expect(item(container, KEYS.subnetA)).toHaveFocus()
    // a nested treeitem key press must not be handled by its ancestors
    expect(item(container, KEYS.azA)).toHaveAttribute('aria-expanded', 'true')
    await user.keyboard('{End}')
    expect(item(container, KEYS.taskStandalone)).toHaveFocus()
    await user.keyboard('{Home}')
    expect(item(container, KEYS.vpc)).toHaveFocus()
  })

  it('Enter on a service item navigates to /services/:id/overview', async () => {
    const user = userEvent.setup()
    const { container } = renderTree()
    item(container, KEYS.payments).focus()
    await user.keyboard('{Enter}')
    expect(screen.getByTestId('location')).toHaveTextContent('/services/svc-pay/overview')
  })

  it('Enter on a grouping item toggles it', async () => {
    const user = userEvent.setup()
    const toggled: string[] = []
    const { container } = renderTree(new Set(), (key) => toggled.push(key))
    item(container, KEYS.azA).focus()
    await user.keyboard('{Enter}')
    expect(toggled).toEqual([KEYS.azA])
  })
})
