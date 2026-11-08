import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { AwakeGrid } from '../../../api/offhours'
import { awakeHours, presetGrid } from './offhours'
import { WeekGrid } from './WeekGrid'

function Harness({ initial, seen }: { initial: AwakeGrid; seen?: (grid: AwakeGrid) => void }) {
  const [grid, setGrid] = useState(initial)
  return (
    <WeekGrid
      value={grid}
      now={{ day: 5, hour: 23 }}
      onChange={(next) => {
        setGrid(next)
        seen?.(next)
      }}
    />
  )
}

const cell = (name: string) => screen.getByRole('button', { name })

describe('WeekGrid', () => {
  it('renders 168 cells that say whether they are awake', () => {
    render(<Harness initial={presetGrid('Weekdays 07–20')} />)
    expect(screen.getAllByRole('button')).toHaveLength(168)
    expect(cell('Mon 7:00 awake')).toHaveAttribute('aria-pressed', 'true')
    expect(cell('Mon 6:00 asleep')).toHaveAttribute('aria-pressed', 'false')
    expect(cell('Sat 23:00 asleep')).toHaveAttribute('data-now', 'true')
  })

  it('toggles a cell by click and keyboard', async () => {
    const seen: AwakeGrid[] = []
    render(<Harness initial={presetGrid('Weekdays 07–20')} seen={(grid) => seen.push(grid)} />)
    await userEvent.click(cell('Mon 7:00 awake'))
    expect(cell('Mon 7:00 asleep')).toHaveAttribute('aria-pressed', 'false')
    cell('Sun 3:00 asleep').focus()
    await userEvent.keyboard('{Enter}')
    expect(cell('Sun 3:00 awake')).toHaveAttribute('aria-pressed', 'true')
    await userEvent.keyboard(' ')
    expect(cell('Sun 3:00 asleep')).toHaveAttribute('aria-pressed', 'false')
    expect(awakeHours(seen[1])).toBe(65)
  })

  it("paints a drag with the first cell's value", async () => {
    render(<Harness initial={presetGrid('Weekdays 07–20')} />)
    // The first cell is awake, so the drag puts the cells it crosses to sleep, whatever they were.
    await userEvent.pointer([
      { keys: '[MouseLeft>]', target: cell('Mon 7:00 awake') },
      { target: cell('Mon 8:00 awake') },
      { target: cell('Mon 6:00 asleep') },
      { keys: '[/MouseLeft]' },
    ])
    expect(cell('Mon 7:00 asleep')).toHaveAttribute('aria-pressed', 'false')
    expect(cell('Mon 8:00 asleep')).toHaveAttribute('aria-pressed', 'false')
    expect(cell('Mon 6:00 asleep')).toHaveAttribute('aria-pressed', 'false')
    expect(cell('Mon 9:00 awake')).toHaveAttribute('aria-pressed', 'true')

    // After the button is released, moving over cells changes nothing.
    await userEvent.pointer([{ target: cell('Tue 9:00 awake') }])
    expect(cell('Tue 9:00 awake')).toHaveAttribute('aria-pressed', 'true')

    // A drag that starts on a sleeping cell wakes the cells it crosses.
    await userEvent.pointer([{ keys: '[MouseLeft>]', target: cell('Tue 2:00 asleep') }, { target: cell('Tue 3:00 asleep') }, { keys: '[/MouseLeft]' }])
    expect(cell('Tue 2:00 awake')).toHaveAttribute('aria-pressed', 'true')
    expect(cell('Tue 3:00 awake')).toHaveAttribute('aria-pressed', 'true')
  })

  it('arrow keys move focus', async () => {
    render(<Harness initial={presetGrid('Always on')} />)
    await userEvent.tab()
    expect(cell('Mon 0:00 awake')).toHaveFocus()
    await userEvent.keyboard('{ArrowRight}{ArrowRight}')
    expect(cell('Mon 2:00 awake')).toHaveFocus()
    await userEvent.keyboard('{ArrowDown}')
    expect(cell('Tue 2:00 awake')).toHaveFocus()
    await userEvent.keyboard('{ArrowLeft}{ArrowUp}')
    expect(cell('Mon 1:00 awake')).toHaveFocus()
    // The edges stay put instead of wrapping.
    await userEvent.keyboard('{ArrowUp}{ArrowLeft}{ArrowLeft}')
    expect(cell('Mon 0:00 awake')).toHaveFocus()
    // Only the focused cell is a tab stop.
    expect(screen.getAllByRole('button').filter((button) => button.tabIndex === 0)).toHaveLength(1)
  })
})
