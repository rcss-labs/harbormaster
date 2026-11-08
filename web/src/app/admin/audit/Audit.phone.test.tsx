import { fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 50))
})

describe('Audit on a phone', () => {
  it('filters by type with chips and offers Clear filters when nothing matches', async () => {
    renderApp('/settings/audit')
    await screen.findByText('Signed in.')
    fireEvent.click(screen.getByRole('button', { name: 'tag' }))
    expect(screen.getByText('Created tag Prod.')).toBeInTheDocument()
    expect(screen.queryByText('Signed in.')).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Filter audit log'), { target: { value: 'zzz-nothing' } })
    expect(screen.getByText('No entries match.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(await screen.findByText('Signed in.')).toBeInTheDocument()
  })
})
