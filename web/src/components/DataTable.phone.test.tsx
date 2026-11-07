import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installPhoneMedia } from '../test/phone'
import { DataTable, type DataTableColumn } from './DataTable'

interface Row {
  id: string
  name: string
  status: string
  tasks: number
  note: string
}

const longName = 'a-very-long-service-name-that-would-push-the-action-button-off-the-edge-of-a-phone-screen'
const rows: Row[] = [
  { id: 'a', name: 'checkout', status: 'Healthy', tasks: 4, note: 'n/a' },
  { id: 'b', name: longName, status: 'Failing', tasks: 9, note: '' },
]

const columns: DataTableColumn<Row>[] = [
  { key: 'name', header: 'Service', render: (r) => r.name },
  { key: 'status', header: 'Status', render: (r) => r.status },
  { key: 'tasks', header: 'Tasks', phoneLabel: false, sortValue: (r) => r.tasks, render: (r) => `${r.tasks} tasks` },
  { key: 'note', header: 'Note', phoneLabel: false, render: (r) => r.note },
  { key: 'secret', header: 'Secret', phone: 'hidden', render: () => 'hidden-value' },
]

const props = { label: 'Services', columns, rows, rowKey: (r: Row) => r.id }

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('DataTable on phones', () => {
  it('labels a meta value with its column header unless the column opts out or renames it', () => {
    const labelled: DataTableColumn<Row>[] = [
      columns[0],
      columns[1],
      { key: 'count', header: 'Replicas', render: (r) => r.tasks },
      { key: 'renamed', header: 'Replica count', phoneLabel: 'Reps', render: (r) => r.tasks },
      { key: 'plain', header: 'Plain', phoneLabel: false, render: () => 'as-is' },
    ]
    render(<DataTable {...props} columns={labelled} />)
    const first = screen.getAllByRole('row')[0]
    expect(first.querySelector('[data-hm-cell="count"]')).toHaveTextContent('Replicas 4')
    expect(first.querySelector('[data-hm-cell="renamed"]')).toHaveTextContent('Reps 4')
    expect(first.querySelector('[data-hm-cell="plain"]')?.textContent).toBe('as-is')
    // Title and status stay clean.
    expect(first.querySelector('[data-hm-cell="name"]')?.textContent).toBe('checkout')
    expect(first.querySelector('[data-hm-cell="status"]')?.textContent).toBe('Healthy')
  })

  it('phoneSort adds a sort sheet that reorders the rows', async () => {
    const user = userEvent.setup()
    render(<DataTable {...props} phoneSort />)
    expect(screen.getAllByRole('row')[0]).toHaveTextContent('checkout')
    await user.click(screen.getByRole('button', { name: 'Sort and fields' }))
    const sheet = screen.getByRole('dialog', { name: 'Sort and fields' })
    await user.click(within(within(sheet).getByRole('group', { name: 'Sort by' })).getByRole('button', { name: 'Tasks' }))
    await user.click(within(sheet).getByRole('button', { name: 'Descending' }))
    expect(screen.getAllByRole('row')[0]).toHaveTextContent(longName)
  })

  it('shows no sort button when no column is sortable', () => {
    render(<DataTable {...props} columns={columns.map((column) => ({ ...column, sortValue: undefined }))} phoneSort />)
    expect(screen.queryByRole('button', { name: 'Sort and fields' })).not.toBeInTheDocument()
  })

  it('renders rows with title, status and meta cells and the action button', () => {
    render(<DataTable {...props} rowAction={(r) => <button type="button">Actions for {r.name}</button>} />)
    const table = screen.getByRole('table', { name: 'Services' })
    expect(within(table).queryAllByRole('columnheader')).toHaveLength(0)
    const [first] = within(table).getAllByRole('row')
    expect(first).toHaveAttribute('data-hm-row')
    expect(first.querySelector('.hm-l1 [data-hm-cell="name"]')).toHaveTextContent('checkout')
    expect(first.querySelector('.hm-l1 [data-hm-cell="status"]')).toHaveTextContent('Healthy')
    expect(first.querySelector('.hm-l2 [data-hm-cell="tasks"]')).toHaveTextContent('4 tasks')
    expect(first.querySelector('.hm-l2 [data-hm-cell="note"]')).toHaveTextContent('n/a')
    expect(within(first).getByRole('button', { name: 'Actions for checkout' })).toBeInTheDocument()
    // A header is not rendered, so no column labels appear in the rows.
    expect(within(table).queryByText('Service')).not.toBeInTheDocument()
  })

  it('long title wraps and the action button keeps its column', () => {
    render(<DataTable {...props} rowAction={(r) => <button type="button">Actions for {r.name}</button>} />)
    const row = screen.getAllByRole('row')[1]
    const title = row.querySelector('[data-hm-cell="name"]') as HTMLElement
    expect(title).toHaveTextContent(longName)
    expect(title).toHaveClass('hm-nm')
    expect(row.lastElementChild).toHaveAttribute('data-hm-cell', '__actions')
    expect(row.lastElementChild).toContainElement(within(row).getByRole('button'))
    // An empty meta value leaves no empty cell behind.
    expect(row.querySelector('[data-hm-cell="note"]')).toBeNull()
  })

  it('a column marked phone action fills the action column instead of line 2', () => {
    const withActions: DataTableColumn<Row>[] = [...columns.slice(0, 3), { key: 'actions', header: '', phone: 'action', render: (r) => <button type="button">Menu {r.name}</button> }]
    render(<DataTable {...props} columns={withActions} />)
    const row = screen.getAllByRole('row')[0]
    expect(row.lastElementChild).toContainElement(within(row).getByRole('button', { name: 'Menu checkout' }))
    expect(row.querySelector('.hm-l2 [data-hm-cell="actions"]')).toBeNull()
  })

  it('hidden phone columns are omitted', () => {
    render(<DataTable {...props} />)
    expect(screen.queryByText('hidden-value')).not.toBeInTheDocument()
  })

  it('an explicit title column wins over position', () => {
    const reordered: DataTableColumn<Row>[] = [{ ...columns[2], phone: 'meta' }, { ...columns[0], phone: 'title' }]
    render(<DataTable {...props} columns={reordered} />)
    const row = screen.getAllByRole('row')[0]
    expect(row.querySelector('.hm-l1 [data-hm-cell="name"]')).toHaveTextContent('checkout')
    expect(row.querySelector('.hm-l2 [data-hm-cell="tasks"]')).toBeInTheDocument()
  })

  it('onRowClick makes the row a button', async () => {
    const user = userEvent.setup()
    const onRowClick = vi.fn()
    const onAction = vi.fn()
    render(<DataTable {...props} onRowClick={onRowClick} rowAction={() => <button type="button" onClick={onAction}>Actions</button>} />)
    const row = screen.getAllByRole('row')[0]
    expect(row).toHaveAttribute('tabindex', '0')
    await user.click(within(row).getByText('4 tasks'))
    expect(onRowClick).toHaveBeenCalledWith(rows[0])
    onRowClick.mockClear()
    // Controls inside the row keep their own click.
    await user.click(within(row).getByRole('button', { name: 'Actions' }))
    expect(onAction).toHaveBeenCalledOnce()
    expect(onRowClick).not.toHaveBeenCalled()
    row.focus()
    await user.keyboard('{Enter}')
    expect(onRowClick).toHaveBeenCalledWith(rows[0])
  })

  it('pager renders Prev/Next at 44 px class', async () => {
    const user = userEvent.setup()
    render(<DataTable {...props} pageSize={1} />)
    const pager = screen.getByRole('navigation', { name: 'Pagination' })
    expect(pager.className).toContain('phone')
    expect(pager).toHaveTextContent('1 of 2')
    expect(screen.getAllByRole('row')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    expect(pager).toHaveTextContent('2 of 2')
    expect(screen.getByRole('row')).toHaveTextContent(longName)
  })

  it('shows the empty content when there are no rows', () => {
    render(<DataTable {...props} rows={[]} empty={<p>Nothing here</p>} />)
    expect(screen.getByText('Nothing here')).toBeInTheDocument()
  })
})
