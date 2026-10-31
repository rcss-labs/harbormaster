import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { ME_OMER } from '../../test/fixtures'
import { renderApp } from '../../test/renderApp'
import { server } from '../../test/msw'

describe('MyAccess', () => {
  it('user sees ✓ for granted actions only', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/me/access')

    // Columns: Tag, View, Logs, Operate, Deploy, Edit task definition, Terminal, Services.
    const devRow = (await screen.findByText('Dev')).closest('tr') as HTMLElement
    const devCells = within(devRow).getAllByRole('cell').map((cell) => cell.textContent)
    expect(devCells.slice(0, 7)).toEqual(['Dev', '✓', '✓', '✓', '–', '–', '–'])

    const stagingRow = screen.getByText('Staging').closest('tr') as HTMLElement
    const stagingCells = within(stagingRow).getAllByRole('cell').map((cell) => cell.textContent)
    expect(stagingCells.slice(0, 7)).toEqual(['Staging', '✓', '–', '–', '–', '–', '–'])
  })
})
