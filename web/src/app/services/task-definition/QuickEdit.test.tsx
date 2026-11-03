import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { ImageTagsResponse } from '../../../api/change'
import { QuickEdit } from './QuickEdit'
import type { Definition } from './mainContainer'

const ecr: ImageTagsResponse = {
  source: 'ecr',
  container: 'payments-api',
  image: 'payments-api',
  repository: 'payments-api',
  current: '0.4.2',
  total: 2,
  matched: 2,
  truncated: false,
  tags: [
    { tag: '0.4.4', pushed_at: '2026-10-30T10:00:00Z' },
    { tag: '0.4.2', pushed_at: '2026-10-20T10:00:00Z' },
  ],
}
const none: ImageTagsResponse = { source: 'none', container: 'payments-api', image: '', repository: '', current: '', total: 0, matched: 0, truncated: false, tags: [] }

const base: Definition = {
  family: 'payments-api',
  cpu: '256',
  memory: '512',
  containerDefinitions: [{ name: 'payments-api', image: 'payments-api:0.4.2', essential: true, environment: [{ name: 'A', value: '1' }] }],
}

function Harness({ tags, oomHint }: { tags: ImageTagsResponse; oomHint?: { memory: string; suggested: string } }) {
  const [definition, setDefinition] = useState(base)
  return (
    <>
      <QuickEdit definition={definition} mainContainer="payments-api" imageTags={tags} deployedTag="0.4.2" oomHint={oomHint} disabled={false} onChange={setDefinition} />
      <output data-testid="json">{JSON.stringify(definition)}</output>
    </>
  )
}

describe('QuickEdit', () => {
  it('lists ECR tags marked newest and deployed', () => {
    render(<Harness tags={ecr} />)
    expect(screen.getByRole('option', { name: '0.4.4 · newest' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: '0.4.2 · deployed' })).toBeInTheDocument()
  })

  it('rejects an invalid free-text tag', async () => {
    render(<Harness tags={none} />)
    const input = screen.getByRole('textbox', { name: 'Image tag' })
    await userEvent.clear(input)
    await userEvent.type(input, 'bad tag!')
    expect(screen.getByText('Tag may contain letters, digits, . _ -')).toBeInTheDocument()
    expect(screen.getByTestId('json').textContent).toContain('payments-api:bad tag!')
  })

  it('offers the Fargate memory values for the chosen CPU plus the current value', async () => {
    render(<Harness tags={ecr} />)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Task CPU' }), '512')
    const memory = screen.getByRole('combobox', { name: 'Task memory' })
    expect(Array.from(memory.querySelectorAll('option')).map((option) => option.textContent)).toEqual(['512', '1024', '2048', '3072', '4096'])
    expect(screen.getByTestId('json').textContent).toContain('"memory":"512"')
  })

  it('keeps a memory value that is invalid for the new CPU instead of snapping it', async () => {
    const big: Definition = { ...base, cpu: '1024', memory: '8192' }
    function Big() {
      const [definition, setDefinition] = useState(big)
      return (
        <>
          <QuickEdit definition={definition} mainContainer="payments-api" imageTags={ecr} deployedTag="0.4.2" disabled={false} onChange={setDefinition} />
          <output data-testid="json">{JSON.stringify(definition)}</output>
        </>
      )
    }
    render(<Big />)
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Task CPU' }), '512')
    expect(screen.getByTestId('json').textContent).toContain('"memory":"8192"')
    expect(within(screen.getByRole('combobox', { name: 'Task memory' })).getByRole('option', { name: '8192' })).toBeInTheDocument()
  })

  it('shows the out of memory hint', () => {
    render(<Harness tags={ecr} oomHint={{ memory: '512', suggested: '1024' }} />)
    expect(screen.getByText('Tasks of this service stop with OutOfMemoryError (exit 137) at 512 MiB. Try 1024 MiB.')).toBeInTheDocument()
  })

  it('adds an environment variable', async () => {
    render(<Harness tags={ecr} />)
    await userEvent.click(screen.getByRole('button', { name: 'Add variable' }))
    expect(screen.getByTestId('json').textContent).toContain('NEW_VAR')
  })
})
