import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { useAnalyticsPrefs } from './prefs'

const KEY = 'harbormaster-analytics-prefs'

afterEach(() => localStorage.clear())

describe('useAnalyticsPrefs', () => {
  it('defaults showSources to true', () => {
    const { result } = renderHook(() => useAnalyticsPrefs())
    expect(result.current[0]).toEqual({ showSources: true })
  })

  it('persists a change to localStorage', () => {
    const { result } = renderHook(() => useAnalyticsPrefs())
    act(() => result.current[1]({ showSources: false }))
    expect(result.current[0]).toEqual({ showSources: false })
    expect(JSON.parse(localStorage.getItem(KEY)!)).toEqual({ showSources: false })
    const again = renderHook(() => useAnalyticsPrefs())
    expect(again.result.current[0].showSources).toBe(false)
  })

  it('survives bad JSON', () => {
    localStorage.setItem(KEY, '{not json')
    const { result } = renderHook(() => useAnalyticsPrefs())
    expect(result.current[0]).toEqual({ showSources: true })
  })

  it('ignores stored values of the wrong type and keys it no longer has', () => {
    localStorage.setItem(KEY, JSON.stringify({ showSources: 'no', showOld: false }))
    const { result } = renderHook(() => useAnalyticsPrefs())
    expect(result.current[0]).toEqual({ showSources: true })
  })
})
