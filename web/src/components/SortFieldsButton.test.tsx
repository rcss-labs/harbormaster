import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import type { SortState } from './DataTable'
import { SortFieldsButton } from './SortFieldsButton'

const sortOptions = [
  { key: 'name', label: 'Name' },
  { key: 'tasks', label: 'Tasks' },
]
const columns = [
  { key: 'name', label: 'Name', locked: true },
  { key: 'tasks', label: 'Tasks' },
  { key: 'image', label: 'Image' },
]

function Host({ onSort, onVisible }: { onSort?: (s: SortState) => void; onVisible?: (keys: string[]) => void }) {
  const [sort, setSort] = useState<SortState | null>({ key: 'name', dir: 'asc' })
  const [visible, setVisible] = useState(['name', 'tasks'])
  return (
    <SortFieldsButton
      sortOptions={sortOptions}
      sort={sort}
      onSortChange={(next) => {
        setSort(next)
        onSort?.(next)
      }}
      columns={columns}
      visible={visible}
      onVisibleChange={(keys) => {
        setVisible(keys)
        onVisible?.(keys)
      }}
      defaults={{ sort: { key: 'name', dir: 'asc' }, visible: ['name', 'tasks', 'image'] }}
    />
  )
}

describe('SortFieldsButton', () => {
  it('opens a sheet with sort chips, direction, field toggles, reset and Done', async () => {
    const user = userEvent.setup()
    const onSort = vi.fn()
    const onVisible = vi.fn()
    render(<Host onSort={onSort} onVisible={onVisible} />)
    const opener = screen.getByRole('button', { name: 'Sort and fields' })
    expect(opener).toHaveAttribute('aria-haspopup', 'dialog')
    await user.click(opener)

    const sheet = screen.getByRole('dialog', { name: 'Sort and fields' })
    const sortBy = within(sheet).getByRole('group', { name: 'Sort by' })
    expect(within(sortBy).getByRole('button', { name: 'Name' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(within(sortBy).getByRole('button', { name: 'Tasks' }))
    expect(onSort).toHaveBeenLastCalledWith({ key: 'tasks', dir: 'asc' })

    const direction = within(sheet).getByRole('group', { name: 'Direction' })
    await user.click(within(direction).getByRole('button', { name: 'Descending' }))
    expect(onSort).toHaveBeenLastCalledWith({ key: 'tasks', dir: 'desc' })
    expect(within(direction).getByRole('button', { name: 'Descending' })).toHaveAttribute('aria-pressed', 'true')

    expect(sheet).toHaveTextContent('2 of 3')
    await user.click(within(sheet).getByRole('button', { name: 'Image' }))
    expect(onVisible).toHaveBeenLastCalledWith(['name', 'tasks', 'image'])
    expect(sheet).toHaveTextContent('3 of 3')
    // A locked field cannot be switched off.
    const fields = within(sheet).getByRole('group', { name: 'Fields on each row' })
    expect(within(fields).getByRole('button', { name: 'Name' })).toBeDisabled()
  })

  it('Reset to default restores sort and fields; Done closes', async () => {
    const user = userEvent.setup()
    const onSort = vi.fn()
    const onVisible = vi.fn()
    render(<Host onSort={onSort} onVisible={onVisible} />)
    await user.click(screen.getByRole('button', { name: 'Sort and fields' }))
    const sheet = screen.getByRole('dialog')
    await user.click(within(within(sheet).getByRole('group', { name: 'Sort by' })).getByRole('button', { name: 'Tasks' }))
    await user.click(within(sheet).getByRole('button', { name: 'Image' }))
    await user.click(within(sheet).getByRole('button', { name: 'Image' }))
    onSort.mockClear()
    await user.click(within(sheet).getByRole('button', { name: 'Reset to default' }))
    expect(onSort).toHaveBeenCalledWith({ key: 'name', dir: 'asc' })
    expect(onVisible).toHaveBeenLastCalledWith(['name', 'tasks', 'image'])

    await user.click(within(sheet).getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('without columns offers only the sort controls', async () => {
    const user = userEvent.setup()
    render(<SortFieldsButton sortOptions={sortOptions} sort={null} onSortChange={() => {}} />)
    await user.click(screen.getByRole('button', { name: 'Sort and fields' }))
    const sheet = screen.getByRole('dialog')
    expect(sheet).not.toHaveTextContent('Fields on each row')
    expect(within(sheet).getByRole('button', { name: 'Name' })).toHaveAttribute('aria-pressed', 'false')
  })
})
