import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import { describe, expect, it } from 'vitest'
import type { Definition } from '../task-definition/mainContainer'
import { CommandEditor } from './CommandEditor'
import { readArgv, setCommand } from './draft'

let latest: Definition

function Harness() {
  const [definition, setDefinition] = useState<Definition>({ containerDefinitions: [{ name: 'app', image: 'app:1', command: ['serve'] }] })
  useEffect(() => {
    latest = definition
  })
  return <CommandEditor label="Command" argv={readArgv(definition, 'app', 'command')} onChange={(argv) => setDefinition(setCommand(definition, 'app', argv))} />
}

const app = () => (latest.containerDefinitions as Record<string, unknown>[])[0]

describe('CommandEditor', () => {
  it('writes argv and removes the key when emptied', async () => {
    const user = userEvent.setup()
    render(<Harness />)

    expect(screen.getByRole('textbox', { name: 'Command argument 1' })).toHaveValue('serve')
    await user.click(screen.getByRole('button', { name: 'Add Command argument' }))
    await user.type(screen.getByRole('textbox', { name: 'Command argument 2' }), '--port=8080')
    expect(app().command).toEqual(['serve', '--port=8080'])

    await user.click(screen.getByRole('button', { name: 'Remove Command argument 2' }))
    await user.click(screen.getByRole('button', { name: 'Remove Command argument 1' }))
    expect('command' in app()).toBe(false)
    expect(screen.getByText(/image default/i)).toBeInTheDocument()
  })
})
