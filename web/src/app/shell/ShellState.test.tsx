import { renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { applyStoredScale, SCALE_STORAGE_KEY, setScale } from '../scale'
import { ShellStateProvider, useShellState } from './ShellState'

const setWindowWidth = (width: number) => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: width })
}
const originalWidth = window.innerWidth
const wrapper = ({ children }: { children: ReactNode }) => <ShellStateProvider>{children}</ShellStateProvider>

beforeEach(() => localStorage.clear())
afterEach(() => {
  setScale('auto')
  delete document.documentElement.dataset.scale
  setWindowWidth(originalWidth)
})

describe('ShellState with a UI scale', () => {
  it('sidebar auto-collapse uses the layout width', () => {
    // 1500 px window at 150 % lays out like 1000 px, which is under the 1100 px threshold.
    setWindowWidth(1500)
    localStorage.setItem(SCALE_STORAGE_KEY, '150')
    const stop = applyStoredScale()
    const { result } = renderHook(() => useShellState(), { wrapper })
    expect(result.current.layoutWidth).toBe(1000)
    expect(result.current.windowWidth).toBe(1500)
    expect(result.current.sidebarCollapsed).toBe(true)
    stop()
  })

  it('a 100 % scale leaves the width alone', () => {
    setWindowWidth(1500)
    const stop = applyStoredScale()
    const { result } = renderHook(() => useShellState(), { wrapper })
    expect(result.current.layoutWidth).toBe(1500)
    expect(result.current.sidebarCollapsed).toBe(false)
    stop()
  })
})
