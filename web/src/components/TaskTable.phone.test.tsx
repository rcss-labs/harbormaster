import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ALL_TASKS } from '../test/fixtures'
import { installPhoneMedia } from '../test/phone'
import { renderWithProviders } from '../test/setup'
import { TaskTable } from './TaskTable'

const running = { ...ALL_TASKS.find((task) => task.service && task.status === 'RUNNING')!, exec_enabled: true, can: { operate: true, terminal: true } }
const noop = () => {}

let restore: () => void
beforeEach(() => {
  restore = installPhoneMedia(true)
})
afterEach(() => restore())

describe('TaskTable on a phone', () => {
  it('shows rows with the id, status and the default fields only, and no column headers', () => {
    renderWithProviders(<TaskTable items={[running]} withService={false} onMore={noop} onOpen={noop} onStop={noop} />)
    const row = screen.getAllByRole('row')[0]
    expect(within(row).getByRole('button', { name: /^Open details for task / })).toBeInTheDocument()
    expect(within(row).getByText('Running')).toBeInTheDocument()
    expect(screen.queryByRole('columnheader')).not.toBeInTheDocument()
    expect(within(row).queryByText(running.private_ip)).not.toBeInTheDocument()
  })

  it('the row sheet opens Details, and Stop task hands the task to the host', async () => {
    const onOpen = vi.fn()
    const onStop = vi.fn()
    renderWithProviders(<TaskTable items={[running]} withService={false} onMore={noop} onOpen={onOpen} onStop={onStop} />)
    const label = `Actions for task ${running.short_id.slice(0, 12)}`
    await userEvent.click(screen.getByRole('button', { name: label }))
    await userEvent.click(within(await screen.findByRole('menu', { name: label })).getByRole('menuitem', { name: 'Details' }))
    expect(onOpen).toHaveBeenCalledWith(running)

    await userEvent.click(screen.getByRole('button', { name: label }))
    await userEvent.click(within(await screen.findByRole('menu', { name: label })).getByRole('menuitem', { name: 'Stop task' }))
    expect(onStop).toHaveBeenCalledWith(running)
  })

  it('a stopped task has no Terminal or Stop task in its sheet', async () => {
    const stopped = { ...running, status: 'STOPPED', stop_kind: 'error', exit_code: 1, stopped_at: '2026-01-01T00:00:00Z' } as const
    renderWithProviders(<TaskTable items={[stopped]} withService={false} onMore={noop} onOpen={noop} onStop={noop} />)
    const label = `Actions for task ${stopped.short_id.slice(0, 12)}`
    await userEvent.click(screen.getByRole('button', { name: label }))
    const menu = await screen.findByRole('menu', { name: label })
    expect(within(menu).queryByRole('menuitem', { name: /Terminal|Stop task/ })).not.toBeInTheDocument()
  })
})
