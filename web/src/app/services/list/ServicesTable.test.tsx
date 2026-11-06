import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { ServicesMetricsDTO } from '../../../api/types'
import { SERVICES_LIST, SERVICES_METRICS } from '../../../test/fixtures'
import { DEFAULT_COLUMNS, ServicesTable } from './ServicesTable'

const noop = () => {}

function renderTable(metrics: ServicesMetricsDTO | undefined, metricsFailed = false) {
  return render(
    <MemoryRouter>
      <ServicesTable
        services={SERVICES_LIST.items}
        visibleColumns={DEFAULT_COLUMNS}
        sort={{ key: 'name', dir: 'asc' }}
        onSortChange={noop}
        page={1}
        onPageChange={noop}
        isPinned={() => false}
        onTogglePin={noop}
        onDetails={noop}
        onRestart={noop}
        metrics={metrics}
        metricsFailed={metricsFailed}
        empty="none"
      />
    </MemoryRouter>,
  )
}

/** The CPU and Memory cells of the row whose name link is `name`. */
function figures(name: string) {
  const row = screen.getAllByRole('row').find((candidate) => within(candidate).queryByRole('link', { name })) as HTMLElement
  const cells = within(row).getAllByRole('cell')
  const headers = screen.getAllByRole('columnheader').map((header) => header.textContent ?? '')
  return {
    cpu: cells[headers.findIndex((header) => header.startsWith('CPU'))],
    memory: cells[headers.findIndex((header) => header.startsWith('Memory'))],
  }
}

describe('ServicesTable CPU and Memory columns', () => {
  it('CPU and Memory cells from the batch fixture', () => {
    renderTable(SERVICES_METRICS)
    expect(figures('payments-api').cpu).toHaveTextContent('41 %')
    expect(figures('payments-api').memory).toHaveTextContent('63 %')
    expect(figures('notifications').cpu).toHaveTextContent('12 %')
    expect(figures('notifications').memory).toHaveTextContent('35 %')
  })

  it('dash with the Insights-off title, never zero', () => {
    renderTable(SERVICES_METRICS)
    const { cpu, memory } = figures('edge-nginx')
    for (const cell of [cpu, memory]) {
      expect(cell).toHaveTextContent('–')
      expect(cell).not.toHaveTextContent('0')
      expect(within(cell).getByTitle('Container Insights is off for this cluster')).toBeInTheDocument()
    }
  })

  it('dash with the error title for a failed target', () => {
    renderTable(SERVICES_METRICS)
    expect(within(figures('api').cpu).getByTitle('CloudWatch is throttling requests.')).toHaveTextContent('–')
    expect(within(figures('web').memory).getByTitle('CloudWatch is throttling requests.')).toHaveTextContent('–')
  })

  it('dash with "No data yet" while nothing is measured, and the error title when the request failed', () => {
    const { unmount } = renderTable(undefined)
    expect(within(figures('payments-api').cpu).getByTitle('No data yet')).toHaveTextContent('–')
    unmount()
    renderTable(undefined, true)
    expect(within(figures('payments-api').cpu).getByTitle('CloudWatch returned an error.')).toHaveTextContent('–')
  })

  it('columns not sortable', () => {
    renderTable(SERVICES_METRICS)
    for (const header of [screen.getByRole('columnheader', { name: 'CPU' }), screen.getByRole('columnheader', { name: 'Memory' })]) {
      expect(within(header).queryByRole('button')).not.toBeInTheDocument()
      expect(header).not.toHaveAttribute('aria-sort', 'ascending')
      expect(header).not.toHaveAttribute('aria-sort', 'descending')
      expect(header).not.toHaveAttribute('aria-sort', 'none')
    }
  })
})
