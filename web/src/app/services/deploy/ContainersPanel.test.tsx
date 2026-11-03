import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect, useState } from 'react'
import { describe, expect, it } from 'vitest'
import { imageTagsHandler } from '../../../test/imageTags'
import { server } from '../../../test/msw'
import { renderWithProviders } from '../../../test/setup'
import type { Definition } from '../task-definition/mainContainer'
import { ContainersPanel } from './ContainersPanel'

const deployed: Definition = {
  family: 'api',
  cpu: '512',
  memory: '1024',
  containerDefinitions: [
    { name: 'payments-api', image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.2', essential: true },
    { name: 'migrator', image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/migrator:2.2.1', essential: false },
  ],
}

let latest: Definition

function Harness({ canEdit = true, definition = deployed }: { canEdit?: boolean; definition?: Definition }) {
  const [draft, setDraft] = useState(definition)
  useEffect(() => {
    latest = draft
  })
  return <ContainersPanel serviceId="svc" definition={draft} deployed={deployed} onChange={setDraft} canEdit={canEdit} isAdmin={false} tagName="prod" />
}

const imageOf = (def: Definition, name: string) => (def.containerDefinitions as { name: string; image: string }[]).find((entry) => entry.name === name)?.image

describe('ContainersPanel', () => {
  it('switching container edits only that container', async () => {
    server.use(imageTagsHandler())
    const user = userEvent.setup()
    renderWithProviders(<Harness />)

    expect(screen.getByRole('combobox', { name: 'Image tag' })).toHaveValue('1.18.2')
    expect(screen.getByText(/482109375512\.dkr\.ecr\.us-east-1\.amazonaws\.com\/payments-api/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'migrator' }))
    const input = screen.getByRole('combobox', { name: 'Image tag' })
    expect(input).toHaveValue('2.2.1')
    await user.click(input)
    await user.click(await screen.findByRole('option', { name: /2\.3\.0/ }))

    expect(imageOf(latest, 'migrator')).toBe('482109375512.dkr.ecr.us-east-1.amazonaws.com/migrator:2.3.0')
    expect(imageOf(latest, 'payments-api')).toBe(imageOf(deployed, 'payments-api'))
    expect(screen.getByRole('button', { name: 'migrator' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('has no container switcher with a single container', () => {
    server.use(imageTagsHandler())
    renderWithProviders(<Harness definition={{ ...deployed, containerDefinitions: [(deployed.containerDefinitions as unknown[])[0]] }} />)
    expect(screen.queryByRole('group', { name: 'Container' })).not.toBeInTheDocument()
  })

  it('is locked without Edit, with the reason as title', () => {
    server.use(imageTagsHandler())
    renderWithProviders(<Harness canEdit={false} />)
    expect(screen.getByTitle('You need Edit on prod')).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Image tag' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Add variable' })).toBeDisabled()
  })
})
