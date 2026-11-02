import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { AUDIT } from '../../../test/fixtures'
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
})
