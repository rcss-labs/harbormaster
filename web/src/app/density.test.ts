import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { applyStoredDensity, DENSITY_STORAGE_KEY, readDensity, toggleDensity, useDensity } from './density'

describe('density', () => {
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.density
  })
  afterEach(() => {
    delete document.documentElement.dataset.density
  })

  it('defaults to compact', () => {
    expect(readDensity()).toBe('compact')
  })

  it('toggle writes harbormaster-density and data-density', () => {
    expect(toggleDensity()).toBe('comfortable')
    expect(localStorage.getItem('harbormaster-density')).toBe('comfortable')
    expect(DENSITY_STORAGE_KEY).toBe('harbormaster-density')
    expect(document.documentElement.dataset.density).toBe('comfortable')
    expect(toggleDensity()).toBe('compact')
    expect(document.documentElement.dataset.density).toBe('compact')
  })

  it('applyStoredDensity restores the remembered choice and ignores junk', () => {
    localStorage.setItem('harbormaster-density', 'comfortable')
    applyStoredDensity()
    expect(document.documentElement.dataset.density).toBe('comfortable')
    delete document.documentElement.dataset.density
    localStorage.setItem('harbormaster-density', 'huge')
    applyStoredDensity()
    expect(document.documentElement.dataset.density).toBeUndefined()
  })

  it('useDensity follows the toggle', async () => {
    const { result } = renderHook(() => useDensity())
    expect(result.current).toBe('compact')
    act(() => {
      toggleDensity()
    })
    await waitFor(() => expect(result.current).toBe('comfortable'))
  })
})
