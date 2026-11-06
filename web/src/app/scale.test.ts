import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installPhoneMedia } from '../test/phone'
import { applyStoredScale, readScale, resolveScale, SCALE_STORAGE_KEY, setScale, useScale } from './scale'

const setWindowWidth = (width: number) => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: width })
}
const originalWidth = window.innerWidth
const dataScale = () => document.documentElement.dataset.scale

describe('scale', () => {
  let stopListening: (() => void) | undefined
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.scale
    setWindowWidth(1440)
  })
  afterEach(() => {
    stopListening?.()
    stopListening = undefined
    setScale('auto')
    delete document.documentElement.dataset.scale
    setWindowWidth(originalWidth)
    vi.useRealTimers()
  })

  it('auto picks 150 at 3840 and 100 at 2560', () => {
    expect(resolveScale('auto', 3840, false)).toBe(150)
    expect(resolveScale('auto', 3000, false)).toBe(150)
    expect(resolveScale('auto', 2999, false)).toBe(100)
    expect(resolveScale('auto', 2560, false)).toBe(100)
    expect(resolveScale('auto', 1440, false)).toBe(100)
  })

  it('phone never scales', () => {
    expect(resolveScale('200', 390, true)).toBe(100)
    expect(resolveScale('auto', 3840, true)).toBe(100)
    const restore = installPhoneMedia(true)
    try {
      localStorage.setItem(SCALE_STORAGE_KEY, '200')
      stopListening = applyStoredScale()
      expect(dataScale()).toBeUndefined()
    } finally {
      restore()
    }
  })

  it('explicit value wins', () => {
    expect(resolveScale('125', 3840, false)).toBe(125)
    expect(resolveScale('100', 3840, false)).toBe(100)
    expect(resolveScale('200', 1440, false)).toBe(200)
  })

  it('writes harbormaster-scale and data-scale', () => {
    setScale('125')
    expect(localStorage.getItem('harbormaster-scale')).toBe('125')
    expect(SCALE_STORAGE_KEY).toBe('harbormaster-scale')
    expect(dataScale()).toBe('125')
    expect(readScale()).toBe('125')
  })

  it('100 removes the attribute', () => {
    setScale('200')
    expect(dataScale()).toBe('200')
    setScale('100')
    expect(dataScale()).toBeUndefined()
  })

  it('applyStoredScale restores the remembered choice and ignores junk', () => {
    localStorage.setItem(SCALE_STORAGE_KEY, '150')
    stopListening = applyStoredScale()
    expect(dataScale()).toBe('150')
    stopListening()
    localStorage.setItem(SCALE_STORAGE_KEY, 'huge')
    expect(readScale()).toBe('auto')
  })

  it('resize recomputes auto', () => {
    vi.useFakeTimers()
    stopListening = applyStoredScale()
    expect(dataScale()).toBeUndefined()
    setWindowWidth(3840)
    window.dispatchEvent(new Event('resize'))
    expect(dataScale()).toBeUndefined() // debounced
    vi.advanceTimersByTime(150)
    expect(dataScale()).toBe('150')
    setWindowWidth(2560)
    window.dispatchEvent(new Event('resize'))
    vi.advanceTimersByTime(150)
    expect(dataScale()).toBeUndefined()
  })

  it('useScale reports the choice and the effective value', () => {
    setWindowWidth(3840)
    stopListening = applyStoredScale()
    const { result } = renderHook(() => useScale())
    expect(result.current).toEqual({ scale: 'auto', effective: 150 })
    act(() => setScale('200'))
    expect(result.current).toEqual({ scale: '200', effective: 200 })
  })
})
