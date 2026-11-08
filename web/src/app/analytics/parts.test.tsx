import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderWithProviders } from '../../test/setup'
import { SourcesPanel } from './parts'

const WINDOW = { from: '2026-10-01T00:00:00Z' }

describe('SourcesPanel', () => {
  it('the Kept column says the retention in words, not hours', () => {
    renderWithProviders(
      <SourcesPanel
        window={WINDOW}
        showSources
        sources={[
          { table: 'service_availability_daily', reads: 'Daily availability', kept: '2160h', setting: null, window_used: null },
          { table: 'tasks', reads: 'Stopped tasks', kept: '168h', setting: null, window_used: '30d' },
        ]}
      />,
    )
    expect(screen.getByText('90 days')).toBeInTheDocument()
    expect(screen.getByText('7 days · reads 30d')).toBeInTheDocument()
    expect(screen.queryByText(/2160h/)).not.toBeInTheDocument()
  })
})
