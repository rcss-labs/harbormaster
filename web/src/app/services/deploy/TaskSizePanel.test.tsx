import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { Definition } from '../task-definition/mainContainer'
import { TaskSizePanel } from './TaskSizePanel'

let latest: Definition

function Harness({ canEdit = true, oomHint, definition: initial }: { canEdit?: boolean; oomHint?: { memory: string; suggested: string }; definition?: Definition }) {
  const [definition, setDefinition] = useState<Definition>(initial ?? { cpu: '512', memory: '1024', containerDefinitions: [] })
  useEffect(() => {
    latest = definition
  })
  return <TaskSizePanel definition={definition} deployed={initial ?? { cpu: '512', memory: '1024', containerDefinitions: [] }} onChange={setDefinition} canEdit={canEdit} tagName="prod" oomHint={oomHint} />
}

const memoryValues = () => within(screen.getByRole('combobox', { name: 'Task memory' })).getAllByRole('option').map((option) => option.textContent)

describe('TaskSizePanel', () => {
  it('memory options follow CPU', async () => {
    const user = userEvent.setup()
    render(<Harness />)

    expect(memoryValues()).toEqual(['1024', '2048', '3072', '4096'])
    await user.selectOptions(screen.getByRole('combobox', { name: 'Task CPU' }), '256')
    expect(memoryValues()).toEqual(['512', '1024', '2048'])
    // 1024 is valid for 256 CPU too, so it stays selected.
    expect(latest.cpu).toBe('256')
    expect(latest.memory).toBe('1024')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Task memory' }), '2048')
    expect(latest.memory).toBe('2048')
  })

  it('offers the CPU architecture and says what ARM64 means', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    const select = screen.getByRole('combobox', { name: 'CPU architecture' })
    expect(select).toHaveValue('X86_64')
    expect(screen.queryByText(/ARM64 tasks cost about 20% less/)).not.toBeInTheDocument()
    await user.selectOptions(select, 'ARM64')
    expect(latest.runtimePlatform).toEqual({ cpuArchitecture: 'ARM64', operatingSystemFamily: 'LINUX' })
    expect(screen.getByText('ARM64 tasks cost about 20% less. Every image must have an arm64 build.')).toBeInTheDocument()
  })

  it('hides the architecture field for an EC2 definition', () => {
    render(<Harness definition={{ cpu: '512', memory: '1024', requiresCompatibilities: ['EC2'], containerDefinitions: [] }} />)
    expect(screen.queryByRole('combobox', { name: 'CPU architecture' })).not.toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Task CPU' })).toBeInTheDocument()
  })

  it('hides the architecture field for a Windows definition and locks it without Edit', () => {
    const windows = { cpu: '512', memory: '1024', requiresCompatibilities: ['FARGATE'], runtimePlatform: { operatingSystemFamily: 'WINDOWS_SERVER_2019_CORE' }, containerDefinitions: [] }
    const { unmount } = render(<Harness definition={windows} />)
    expect(screen.queryByRole('combobox', { name: 'CPU architecture' })).not.toBeInTheDocument()
    unmount()
    render(<Harness canEdit={false} />)
    expect(screen.getByRole('combobox', { name: 'CPU architecture' })).toBeDisabled()
  })

  it('shows the out-of-memory hint and honours the lock', () => {
    render(<Harness canEdit={false} oomHint={{ memory: '1024', suggested: '2048' }} />)
    expect(screen.getByText(/OutOfMemoryError .* at 1024 MiB\. Try 2048 MiB/)).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Task CPU' })).toBeDisabled()
    expect(screen.getByTitle('You need Edit on prod')).toBeInTheDocument()
  })

  it('no OOM hint without an OOM stop', () => {
    render(<Harness />)
    expect(screen.queryByText(/OutOfMemoryError/)).not.toBeInTheDocument()
  })
})
