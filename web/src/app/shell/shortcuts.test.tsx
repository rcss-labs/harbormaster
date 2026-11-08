import { render } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useShortcuts, type ShortcutOptions } from './shortcuts'

function Harness({ navigate, options }: { navigate: (to: string) => void; options?: ShortcutOptions }) {
  useShortcuts(navigate, options)
  return (
    <div>
      <input aria-label="typing" />
      <input aria-label="page filter" data-hm-filter="" />
    </div>
  )
}

beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }))
afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ''
})

const setup = (options?: ShortcutOptions) => {
  const navigate = vi.fn()
  render(<Harness navigate={navigate} options={options} />)
  return { navigate, user: userEvent.setup({ advanceTimers: vi.advanceTimersByTime }) }
}

describe('shortcuts', () => {
  it('g o navigates', async () => {
    const { navigate, user } = setup()
    await user.keyboard('go')
    expect(navigate).toHaveBeenCalledWith('/')
    await user.keyboard('gs')
    expect(navigate).toHaveBeenLastCalledWith('/services')
    await user.keyboard('gc')
    expect(navigate).toHaveBeenLastCalledWith('/clusters')
    await user.keyboard('gt')
    expect(navigate).toHaveBeenLastCalledWith('/tasks')
    await user.keyboard('gn')
    expect(navigate).toHaveBeenLastCalledWith('/networks')
    await user.keyboard('ga')
    expect(navigate).toHaveBeenLastCalledWith('/analytics/delivery')
  })

  it('g then timeout then o does nothing', async () => {
    const { navigate, user } = setup()
    await user.keyboard('g')
    vi.advanceTimersByTime(1100)
    await user.keyboard('o')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('g o is ignored inside inputs and dialogs', async () => {
    const { navigate, user } = setup()
    await user.click(document.querySelector('input[aria-label="typing"]')!)
    await user.keyboard('go')
    expect(navigate).not.toHaveBeenCalled()
    ;(document.activeElement as HTMLElement).blur()

    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    dialog.setAttribute('aria-modal', 'true')
    document.body.appendChild(dialog)
    await user.keyboard('go')
    expect(navigate).not.toHaveBeenCalled()
    dialog.remove()
    await user.keyboard('go')
    expect(navigate).toHaveBeenCalledWith('/')
  })

  it('is ignored while the palette is open and with modifier keys', async () => {
    const { navigate, user } = setup({ paletteOpen: true })
    await user.keyboard('go')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('does not fire with Ctrl held', async () => {
    const { navigate, user } = setup()
    await user.keyboard('g{Control>}o{/Control}')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('/ focuses [data-hm-filter]', async () => {
    const { user } = setup()
    await user.keyboard('/')
    expect(document.activeElement).toBe(document.querySelector('[data-hm-filter]'))
  })

  it('/ typed into the filter stays text', async () => {
    const { user } = setup()
    await user.keyboard('/')
    await user.keyboard('a/b')
    expect((document.activeElement as HTMLInputElement).value).toBe('a/b')
  })

  it('? opens help', async () => {
    const openHelp = vi.fn()
    const { user } = setup({ openHelp })
    await user.keyboard('?')
    expect(openHelp).toHaveBeenCalledTimes(1)
  })
})
