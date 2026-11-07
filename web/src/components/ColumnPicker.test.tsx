import { render, renderHook, screen, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ColumnPicker, useColumnPrefs } from './ColumnPicker'

const columns = [
  { key: 'name', label: 'Name', locked: true },
  { key: 'cluster', label: 'Cluster' },
  { key: 'tag', label: 'Tag' },
]

beforeEach(() => localStorage.clear())

async function openPicker() {
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Columns' }))
  return user
}

describe('ColumnPicker', () => {
  it('lists every column as a checkbox reflecting visibility', async () => {
    render(<ColumnPicker columns={columns} visible={['name', 'cluster']} onChange={() => {}} />)
    await openPicker()
    expect(screen.getByRole('checkbox', { name: 'Name' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Cluster' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Tag' })).not.toBeChecked()
  })

  it('locked columns cannot be toggled', async () => {
    const onChange = vi.fn()
    render(<ColumnPicker columns={columns} visible={['name', 'cluster']} onChange={onChange} />)
    const user = await openPicker()
    const locked = screen.getByRole('checkbox', { name: 'Name' })
    expect(locked).toBeDisabled()
    await user.click(locked)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('toggling reports the visible keys in column order', async () => {
    const onChange = vi.fn()
    render(<ColumnPicker columns={columns} visible={['name', 'tag']} onChange={onChange} />)
    const user = await openPicker()
    await user.click(screen.getByRole('checkbox', { name: 'Cluster' }))
    expect(onChange).toHaveBeenLastCalledWith(['name', 'cluster', 'tag'])
    await user.click(screen.getByRole('checkbox', { name: 'Tag' }))
    expect(onChange).toHaveBeenLastCalledWith(['name'])
  })

  it('Reset to default restores the defaults (all columns when none are given)', async () => {
    const onChange = vi.fn()
    const { unmount } = render(<ColumnPicker columns={columns} visible={['name']} onChange={onChange} />)
    const user = await openPicker()
    await user.click(screen.getByRole('button', { name: 'Reset to default' }))
    expect(onChange).toHaveBeenLastCalledWith(['name', 'cluster', 'tag'])
    unmount()
    render(<ColumnPicker columns={columns} visible={['name']} defaults={['name', 'tag']} onChange={onChange} />)
    await openPicker()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Reset to default' }))
    expect(onChange).toHaveBeenLastCalledWith(['name', 'tag'])
  })

  it('works controlled by useColumnPrefs', async () => {
    function Host() {
      const [visible, setVisible] = useColumnPrefs('harbormaster-columns:test', ['name', 'cluster'])
      return (
        <div>
          <ColumnPicker columns={columns} visible={visible} onChange={setVisible} />
          <output>{visible.join(',')}</output>
        </div>
      )
    }
    render(<Host />)
    const user = await openPicker()
    await user.click(screen.getByRole('checkbox', { name: 'Tag' }))
    expect(screen.getByRole('status')).toHaveTextContent('name,cluster,tag')
  })
})

describe('useColumnPrefs', () => {
  it('starts from the defaults and persists changes under the storage key', () => {
    const { result } = renderHook(() => useColumnPrefs('harbormaster-columns:services', ['name', 'tag']))
    expect(result.current[0]).toEqual(['name', 'tag'])
    act(() => result.current[1](['name']))
    expect(result.current[0]).toEqual(['name'])
    expect(JSON.parse(localStorage.getItem('harbormaster-columns:services') ?? 'null')).toEqual(['name'])
  })

  it('restores saved preferences, ignoring junk', () => {
    localStorage.setItem('harbormaster-columns:a', JSON.stringify(['x', 'y']))
    expect(renderHook(() => useColumnPrefs('harbormaster-columns:a', ['name'])).result.current[0]).toEqual(['x', 'y'])
    localStorage.setItem('harbormaster-columns:b', '{not json')
    expect(renderHook(() => useColumnPrefs('harbormaster-columns:b', ['name'])).result.current[0]).toEqual(['name'])
    localStorage.setItem('harbormaster-columns:c', JSON.stringify({ a: 1 }))
    expect(renderHook(() => useColumnPrefs('harbormaster-columns:c', ['name'])).result.current[0]).toEqual(['name'])
  })
})
