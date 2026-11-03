import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import { describe, expect, it } from 'vitest'
import { HealthCheckEditor } from './HealthCheckEditor'
import type { HealthCheck } from './draft'

let latest: HealthCheck | null

function Harness({ initial }: { initial: HealthCheck | null }) {
  const [check, setCheck] = useState(initial)
  useEffect(() => {
    latest = check
  })
  return <HealthCheckEditor check={check} onChange={setCheck} />
}

const start: HealthCheck = { command: ['CMD-SHELL', 'curl -f http://localhost/ || exit 1'], interval: 30, timeout: 5, retries: 3, startPeriod: 0 }

async function enter(user: ReturnType<typeof userEvent.setup>, name: string, value: string) {
  const field = screen.getByRole('spinbutton', { name })
  await user.clear(field)
  await user.type(field, value)
  await user.tab()
  return field
}

describe('HealthCheckEditor', () => {
  it('clamps ranges and can remove the check', async () => {
    const user = userEvent.setup()
    render(<Harness initial={start} />)

    expect(await enter(user, 'Interval (s)', '999')).toHaveValue(300)
    expect(await enter(user, 'Interval (s)', '1')).toHaveValue(5)
    expect(await enter(user, 'Timeout (s)', '99')).toHaveValue(60)
    expect(await enter(user, 'Timeout (s)', '0')).toHaveValue(2)
    expect(await enter(user, 'Retries', '11')).toHaveValue(10)
    expect(await enter(user, 'Retries', '0')).toHaveValue(1)
    expect(await enter(user, 'Start period (s)', '301')).toHaveValue(300)
    expect(await enter(user, 'Start period (s)', '0')).toHaveValue(0)
    expect(latest).toMatchObject({ interval: 5, timeout: 2, retries: 1, startPeriod: 0, command: start.command })

    await user.click(screen.getByRole('button', { name: 'Remove health check' }))
    expect(latest).toBeNull()
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Add health check' }))
    expect(latest?.command[0]).toBe('CMD-SHELL')
  })

  it('switches between CMD-SHELL and CMD', async () => {
    const user = userEvent.setup()
    render(<Harness initial={start} />)
    await user.click(screen.getByRole('button', { name: 'CMD' }))
    expect(latest?.command[0]).toBe('CMD')
  })
})
