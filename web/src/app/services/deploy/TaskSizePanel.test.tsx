import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { Definition } from '../task-definition/mainContainer'
import { TaskSizePanel } from './TaskSizePanel'

let latest: Definition

function Harness({ canEdit = true, oomHint }: { canEdit?: boolean; oomHint?: { memory: string; suggested: string } }) {
  const [definition, setDefinition] = useState<Definition>({ cpu: '512', memory: '1024', containerDefinitions: [] })
  useEffect(() => {
    latest = definition
  })
  return <TaskSizePanel definition={definition} onChange={setDefinition} canEdit={canEdit} tagName="prod" oomHint={oomHint} />
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

  it('shows the out-of-memory hint and honours the lock', () => {
    render(<Harness canEdit={false} oomHint={{ memory: '1024', suggested: '2048' }} />)
    expect(screen.getByText(/OutOfMemoryError .* at 1024 MiB\. Try 2048 MiB/)).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Task CPU' })).toBeDisabled()
    expect(screen.getByTitle('You need Edit on prod')).toBeInTheDocument()
  })
})
