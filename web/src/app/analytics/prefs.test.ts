import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { useAnalyticsPrefs } from './prefs'

const KEY = 'harbormaster-analytics-prefs'

afterEach(() => localStorage.clear())

describe('useAnalyticsPrefs', () => {
  it('defaults showSources and showProposed to true', () => {
    const { result } = renderHook(() => useAnalyticsPrefs())
    expect(result.current[0]).toEqual({ showSources: true, showProposed: true })
  })

  it('persists a change to localStorage and keeps the other flag', () => {
    const { result } = renderHook(() => useAnalyticsPrefs())
    act(() => result.current[1]({ showProposed: false }))
    expect(result.current[0]).toEqual({ showSources: true, showProposed: false })
    expect(JSON.parse(localStorage.getItem(KEY)!)).toEqual({ showSources: true, showProposed: false })
    const again = renderHook(() => useAnalyticsPrefs())
    expect(again.result.current[0].showProposed).toBe(false)
  })

  it('survives bad JSON', () => {
    localStorage.setItem(KEY, '{not json')
    const { result } = renderHook(() => useAnalyticsPrefs())
    expect(result.current[0]).toEqual({ showSources: true, showProposed: true })
  })

  it('ignores stored values of the wrong type', () => {
    localStorage.setItem(KEY, JSON.stringify({ showSources: 'no', showProposed: false }))
    const { result } = renderHook(() => useAnalyticsPrefs())
    expect(result.current[0]).toEqual({ showSources: true, showProposed: false })
  })
})
