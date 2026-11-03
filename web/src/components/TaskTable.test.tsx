import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ALL_TASKS } from '../test/fixtures'
import { renderWithProviders } from '../test/setup'
import { TaskTable } from './TaskTable'

const runningTask = { ...ALL_TASKS.find((task) => task.status === 'RUNNING')!, cpu: '256', memory: '512' }

describe('TaskTable', () => {
  it('shows utilisation of reserved when provided', () => {
    renderWithProviders(
      <TaskTable
        items={[runningTask]}
        withService={false}
        onMore={() => {}}
        utilisation={{ [runningTask.short_id]: { cpu: 12.4, mem: 34.2 } }}
      />,
    )
    const row = screen.getAllByRole('row')[1]
    expect(within(row).getByText('12% of 0.25 vCPU')).toBeInTheDocument()
    expect(within(row).getByText('34% of 512 MiB')).toBeInTheDocument()
  })

  it('marks an expected stop teal with a cause chip, and an error stop red without one', () => {
    const base = { ...runningTask, status: 'STOPPED', exit_code: 1, stopped_at: '2026-01-01T00:00:00Z' } as const
    renderWithProviders(
      <TaskTable
        items={[
          { ...base, id: 'a', stop_kind: 'expected', stop_cause: 'spot' },
          { ...base, id: 'b', stop_kind: 'error', stop_cause: '' },
        ]}
        withService={false}
        onMore={() => {}}
      />,
    )
    const [, expectedRow, errorRow] = screen.getAllByRole('row')
    expect(expectedRow.className).toMatch(/expected/)
    expect(within(expectedRow).getByText('spot')).toBeInTheDocument()
    expect(errorRow.className).toMatch(/bad/)
    expect(within(errorRow).queryByText('spot')).not.toBeInTheDocument()
  })

  it('shows reserved size otherwise', () => {
    renderWithProviders(<TaskTable items={[runningTask]} withService={false} onMore={() => {}} utilisation={{ other: { cpu: 1, mem: 1 } }} />)
    const row = screen.getAllByRole('row')[1]
    expect(within(row).getByText('0.25 vCPU')).toBeInTheDocument()
    expect(within(row).getByText('512 MiB')).toBeInTheDocument()
  })

  it('shows reserved size for a task whose usage is not reported yet', () => {
    renderWithProviders(
      <TaskTable
        items={[runningTask]}
        withService={false}
        onMore={() => {}}
        utilisation={{ [runningTask.short_id]: { cpu: null, mem: null } }}
      />,
    )
    expect(within(screen.getAllByRole('row')[1]).getByText('512 MiB')).toBeInTheDocument()
  })
})
