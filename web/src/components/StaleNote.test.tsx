import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderWithProviders } from '../test/setup'
import { StaleNote } from './StaleNote'

describe('StaleNote', () => {
  it('names the age and the reason', () => {
    renderWithProviders(<StaleNote stale={{ reason: 'throttled', fetched_at: new Date(Date.now() - 5 * 60_000).toISOString() }} />)
    const note = screen.getByText(/Showing data from/)
    expect(note).toHaveTextContent('5 min ago')
    expect(note).toHaveTextContent('throttled')
  })
})
