import { renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { installVisualViewport } from '../test/phone'
import { useVisualViewport } from './useVisualViewport'

const root = document.documentElement

describe('useVisualViewport', () => {
  let restore: (() => void) | undefined
  afterEach(() => {
    restore?.()
    restore = undefined
    root.style.removeProperty('--hm-vvh')
    root.style.removeProperty('--hm-vvt')
  })

  it('writes --hm-vvh and --hm-vvt on resize', () => {
    const viewport = installVisualViewport({ height: 844, offsetTop: 0 })
    restore = viewport.restore
    renderHook(() => useVisualViewport())
    expect(root.style.getPropertyValue('--hm-vvh')).toBe('844px')
    expect(root.style.getPropertyValue('--hm-vvt')).toBe('0px')

    viewport.set({ height: 500, offsetTop: 12 })
    expect(root.style.getPropertyValue('--hm-vvh')).toBe('500px')
    expect(root.style.getPropertyValue('--hm-vvt')).toBe('12px')
  })

  it('is a no-op without visualViewport', () => {
    const original = Object.getOwnPropertyDescriptor(window, 'visualViewport')
    Object.defineProperty(window, 'visualViewport', { configurable: true, value: undefined })
    try {
      expect(() => renderHook(() => useVisualViewport())).not.toThrow()
      expect(root.style.getPropertyValue('--hm-vvh')).toBe('')
    } finally {
      if (original) Object.defineProperty(window, 'visualViewport', original)
      else Reflect.deleteProperty(window, 'visualViewport')
    }
  })

  it('removes the variables on unmount', () => {
    const viewport = installVisualViewport({ height: 600, offsetTop: 0 })
    restore = viewport.restore
    const { unmount } = renderHook(() => useVisualViewport())
    expect(root.style.getPropertyValue('--hm-vvh')).toBe('600px')
    unmount()
    expect(root.style.getPropertyValue('--hm-vvh')).toBe('')
    expect(root.style.getPropertyValue('--hm-vvt')).toBe('')
    viewport.set({ height: 300 })
    expect(root.style.getPropertyValue('--hm-vvh')).toBe('')
  })
})
