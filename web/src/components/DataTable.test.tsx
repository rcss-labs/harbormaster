import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { DataTable, type DataTableColumn, type SortState } from './DataTable'

interface Row {
  id: string
  name: string
  tasks: number
  state: 'ok' | 'bad' | 'warn'
}

const rows: Row[] = [
  { id: 'a', name: 'checkout', tasks: 4, state: 'ok' },
  { id: 'b', name: 'billing', tasks: 9, state: 'bad' },
  { id: 'c', name: 'search', tasks: 1, state: 'warn' },
]

const columns: DataTableColumn<Row>[] = [
  { key: 'name', header: 'Service', width: '2fr', render: (r) => r.name, sortValue: (r) => r.name },
  { key: 'tasks', header: 'Tasks', width: '80px', align: 'right', render: (r) => r.tasks, sortValue: (r) => r.tasks },
  { key: 'note', header: 'Note', render: () => 'n/a' },
]

const names = () =>
  screen
    .getAllByRole('row')
    .slice(1)
    .map((row) => within(row).getAllByRole('cell')[0].textContent)

const props = { label: 'Services', columns, rows, rowKey: (r: Row) => r.id }

describe('DataTable', () => {
  it('exposes table semantics with headers and cells', () => {
    render(<DataTable {...props} />)
    const table = screen.getByRole('table', { name: 'Services' })
    expect(within(table).getAllByRole('columnheader')).toHaveLength(3)
    expect(within(table).getAllByRole('row')).toHaveLength(4)
    expect(names()).toEqual(['checkout', 'billing', 'search'])
  })

  it('shows the empty state when there are no rows', () => {
    render(<DataTable {...props} rows={[]} empty="Nothing here" />)
    expect(screen.getByText('Nothing here')).toBeInTheDocument()
  })

  it('sorts via header buttons and sets aria-sort', async () => {
    render(<DataTable {...props} />)
    expect(screen.getByRole('columnheader', { name: 'Service' })).toHaveAttribute('aria-sort', 'none')
    await userEvent.click(screen.getByRole('button', { name: 'Service' }))
    expect(names()).toEqual(['billing', 'checkout', 'search'])
    expect(screen.getByRole('columnheader', { name: 'Service' })).toHaveAttribute('aria-sort', 'ascending')
    await userEvent.click(screen.getByRole('button', { name: 'Service' }))
    expect(names()).toEqual(['search', 'checkout', 'billing'])
    expect(screen.getByRole('columnheader', { name: 'Service' })).toHaveAttribute('aria-sort', 'descending')
  })

  it('sorts numbers numerically and has no sort button on unsortable columns', async () => {
    render(<DataTable {...props} />)
    expect(screen.queryByRole('button', { name: 'Note' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Tasks' }))
    expect(names()).toEqual(['search', 'checkout', 'billing'])
  })

  it('supports controlled sort', async () => {
    const onSortChange = vi.fn()
    render(<DataTable {...props} sort={{ key: 'tasks', dir: 'desc' }} onSortChange={onSortChange} />)
    expect(names()).toEqual(['billing', 'checkout', 'search'])
    await userEvent.click(screen.getByRole('button', { name: 'Tasks' }))
    expect(onSortChange).toHaveBeenCalledWith({ key: 'tasks', dir: 'asc' })
    expect(names()).toEqual(['billing', 'checkout', 'search'])
  })

  it('works when a screen stores sort in its own state', async () => {
    function Host() {
      const [sort, setSort] = useState<SortState | null>(null)
      return <DataTable {...props} sort={sort} onSortChange={setSort} />
    }
    render(<Host />)
    await userEvent.click(screen.getByRole('button', { name: 'Service' }))
    expect(names()).toEqual(['billing', 'checkout', 'search'])
  })

  it('pages rows and moves with the pager', async () => {
    render(<DataTable {...props} pageSize={2} />)
    expect(names()).toEqual(['checkout', 'billing'])
    await userEvent.click(screen.getByRole('button', { name: 'Next page' }))
    expect(names()).toEqual(['search'])
  })

  it('supports controlled paging', async () => {
    const onPageChange = vi.fn()
    render(<DataTable {...props} pageSize={2} page={2} onPageChange={onPageChange} />)
    expect(names()).toEqual(['search'])
    await userEvent.click(screen.getByRole('button', { name: 'Previous page' }))
    expect(onPageChange).toHaveBeenCalledWith(1)
  })

  it('calls onRowClick for clicks and Enter', async () => {
    const onRowClick = vi.fn()
    render(<DataTable {...props} onRowClick={onRowClick} />)
    await userEvent.click(screen.getByText('billing'))
    expect(onRowClick).toHaveBeenLastCalledWith(rows[1])
    screen.getAllByRole('row')[3].focus()
    await userEvent.keyboard('{Enter}')
    expect(onRowClick).toHaveBeenLastCalledWith(rows[2])
  })

  it('marks non-healthy rows with a status description for assistive tech', () => {
    render(<DataTable {...props} rowStatus={(r) => (r.state === 'ok' ? undefined : r.state === 'bad' ? 'bad' : 'warn')} />)
    const bodyRows = screen.getAllByRole('row').slice(1)
    expect(bodyRows[0]).not.toHaveAttribute('data-status')
    expect(bodyRows[1]).toHaveAttribute('data-status', 'bad')
    expect(bodyRows[2]).toHaveAttribute('data-status', 'warn')
  })

  describe('v4 layout options', () => {
    const rowActionProps = { ...props, rowAction: (r: Row) => <button type="button">Restart {r.name}</button> }

    it('sticky actions column keeps position: sticky and the name column stays first', () => {
      render(<DataTable {...rowActionProps} stickyActions />)
      const header = screen.getAllByRole('columnheader')
      expect(header[0]).toHaveTextContent('Service')
      const firstRow = screen.getAllByRole('row')[1]
      const cells = within(firstRow).getAllByRole('cell')
      expect(cells[0]).toHaveTextContent('checkout')
      expect(cells[0]).not.toHaveStyle({ position: 'sticky' })
      const last = cells[cells.length - 1]
      expect(last).toHaveStyle({ position: 'sticky', right: '0px' })
      expect(within(last).getByRole('button', { name: 'Restart checkout' })).toBeInTheDocument()
      expect(header[header.length - 1]).toHaveStyle({ position: 'sticky', right: '0px' })
    })

    it('rowAction adds an Actions column even without stickyActions, and it is not sticky then', () => {
      render(<DataTable {...rowActionProps} />)
      expect(screen.getByRole('columnheader', { name: 'Actions' })).not.toHaveStyle({ position: 'sticky' })
      expect(screen.getAllByRole('button', { name: /^Restart/ })).toHaveLength(3)
    })

    it('stickyActions without rowAction pins the last declared column', () => {
      render(<DataTable {...props} stickyActions />)
      const cells = within(screen.getAllByRole('row')[1]).getAllByRole('cell')
      expect(cells[cells.length - 1]).toHaveStyle({ position: 'sticky' })
    })

    it('visibleColumns hides the columns that are not listed', () => {
      render(<DataTable {...props} visibleColumns={['name', 'tasks']} />)
      expect(screen.getAllByRole('columnheader')).toHaveLength(2)
      expect(screen.queryByRole('columnheader', { name: 'Note' })).not.toBeInTheDocument()
      expect(within(screen.getAllByRole('row')[1]).getAllByRole('cell')).toHaveLength(2)
    })

    it('row height follows the density token', () => {
      document.documentElement.dataset.density = 'comfortable'
      try {
        render(<DataTable {...props} />)
        const row = screen.getAllByRole('row')[1] as HTMLElement
        expect(row.style.minHeight).toBe('var(--row)')
      } finally {
        delete document.documentElement.dataset.density
      }
    })
  })
})
