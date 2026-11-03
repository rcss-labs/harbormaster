import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ScheduledActionEditor } from './ScheduledActionEditor'

function setup() {
  const onSave = vi.fn()
  render(<ScheduledActionEditor initial={null} existingNames={[]} onSave={onSave} onCancel={() => {}} />)
  return onSave
}

describe('ScheduledActionEditor', () => {
  it('emits cron/at/rate expressions', async () => {
    const user = userEvent.setup()
    const onSave = setup()

    await user.type(screen.getByLabelText('Name'), 'sched')
    await user.selectOptions(screen.getByLabelText('Timezone'), 'UTC')
    await user.type(screen.getByLabelText('Minimum capacity'), '2')

    // cron is the default kind
    await user.clear(screen.getByLabelText('Cron fields'))
    await user.type(screen.getByLabelText('Cron fields'), '0 7 * * ? *')
    await user.click(screen.getByRole('button', { name: 'Save action' }))
    expect(onSave).toHaveBeenLastCalledWith({ name: 'sched', schedule: 'cron(0 7 * * ? *)', timezone: 'UTC', min_capacity: 2, max_capacity: null })

    await user.click(screen.getByRole('button', { name: 'At' }))
    await user.type(screen.getByLabelText('Date and time'), '2026-12-01T09:30')
    await user.click(screen.getByRole('button', { name: 'Save action' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ schedule: 'at(2026-12-01T09:30:00)' }))

    await user.click(screen.getByRole('button', { name: 'Rate' }))
    await user.clear(screen.getByLabelText('Every'))
    await user.type(screen.getByLabelText('Every'), '15')
    await user.selectOptions(screen.getByLabelText('Unit'), 'minutes')
    await user.click(screen.getByRole('button', { name: 'Save action' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ schedule: 'rate(15 minutes)' }))

    await user.clear(screen.getByLabelText('Every'))
    await user.type(screen.getByLabelText('Every'), '1')
    await user.selectOptions(screen.getByLabelText('Unit'), 'hours')
    await user.click(screen.getByRole('button', { name: 'Save action' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ schedule: 'rate(1 hour)' }))
  })

  it('will not save without a capacity or with a malformed cron', async () => {
    const user = userEvent.setup()
    const onSave = setup()
    await user.type(screen.getByLabelText('Name'), 'sched')
    expect(screen.getByRole('button', { name: 'Save action' })).toBeDisabled()
    await user.type(screen.getByLabelText('Maximum capacity'), '4')
    await user.clear(screen.getByLabelText('Cron fields'))
    await user.type(screen.getByLabelText('Cron fields'), '0 7 *')
    expect(screen.getByRole('button', { name: 'Save action' })).toBeDisabled()
    expect(onSave).not.toHaveBeenCalled()
  })
})
