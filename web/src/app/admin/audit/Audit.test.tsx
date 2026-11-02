import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { AuditDTO } from '../../../api/types'
import { AUDIT } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

describe('Audit', () => {
  it('renders summaries newest first', async () => {
    renderApp('/settings/audit')
    expect(await screen.findByRole('heading', { level: 2, name: 'Audit log' })).toBeInTheDocument()
    const rendered = await Promise.all(AUDIT.items.map(async (entry) => screen.findByText(entry.summary)))
    for (let index = 0; index < rendered.length - 1; index++) {
      const isBefore = rendered[index].compareDocumentPosition(rendered[index + 1]) & Node.DOCUMENT_POSITION_FOLLOWING
      expect(isBefore).toBeTruthy()
    }
  })

  it('filters by type (the action prefix)', async () => {
    renderApp('/settings/audit')
    await screen.findByText('Signed in.')
    const type = screen.getByRole('combobox', { name: 'Type' })
    expect(within(type).getAllByRole('option').map((option) => option.textContent)).toEqual([
      'All types',
      'auth',
      'permission',
      'cluster',
      'user',
      'tag',
    ])
    await userEvent.selectOptions(type, 'tag')
    expect(screen.getByText('Created tag Prod.')).toBeInTheDocument()
    expect(screen.queryByText('Signed in.')).not.toBeInTheDocument()
    expect(screen.queryByText('Created user omer with Operator on Dev.')).not.toBeInTheDocument()
  })

  it('filters by text across summary, actor and target', async () => {
    renderApp('/settings/audit')
    await screen.findByText('Signed in.')
    await userEvent.type(screen.getByRole('searchbox', { name: 'Filter audit log' }), 'staging-web')
    expect(screen.getByText('Tagged cluster staging-web as Staging (was Dev).')).toBeInTheDocument()
    expect(screen.queryByText('Signed in.')).not.toBeInTheDocument()
  })

  it('says when nothing matches', async () => {
    renderApp('/settings/audit')
    await screen.findByText('Signed in.')
    await userEvent.type(screen.getByRole('searchbox', { name: 'Filter audit log' }), 'zzz-nothing')
    expect(screen.getByText('No entries match.')).toBeInTheDocument()
  })

  it('Load more appends older entries and keeps the filter', async () => {
    const older: AuditDTO = {
      items: [{ ...AUDIT.items[0], id: 0, action: 'tag.delete', summary: 'Deleted tag Old.' }],
      next: null,
      polled_at: AUDIT.polled_at,
    }
    server.use(
      http.get('/api/audit', ({ request }) =>
        HttpResponse.json(new URL(request.url).searchParams.get('cursor') ? older : { ...AUDIT, next: 'older' }),
      ),
    )
    renderApp('/settings/audit')
    await screen.findByText('Signed in.')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Type' }), 'tag')
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('Deleted tag Old.')).toBeInTheDocument()
    expect(screen.getByText('Created tag Prod.')).toBeInTheDocument()
    expect(screen.queryByText('Signed in.')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  })
})
