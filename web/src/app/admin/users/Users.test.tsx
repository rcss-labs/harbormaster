import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../../../test/msw'
import { USERS } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'
import { PRESETS, normalize, presetOf, toggle } from '../perms'

const OMER_ID = 3
const PROD_TAG_ID = 3

describe('perms', () => {
  it('presetOf and normalize tables (Viewer=1, Operator=7, Deployer=31, Full=63, 34→Custom, toggle view off clears)', () => {
    expect(PRESETS).toEqual({ None: 0, Viewer: 1, Operator: 7, Deployer: 31, Full: 63 })
    expect([0, 1, 7, 31, 63].map(presetOf)).toEqual(['None', 'Viewer', 'Operator', 'Deployer', 'Full'])
    expect(presetOf(34)).toBe('Custom')
    expect(normalize(34)).toBe(35)
    expect(normalize(2)).toBe(3)
    expect(normalize(0)).toBe(0)
    expect(toggle(1, 2)).toBe(3)
    expect(toggle(0, 8)).toBe(9)
    expect(toggle(7, 4)).toBe(3)
    expect(toggle(63, 1)).toBe(0)
  })
})

describe('matrix', () => {
  const omerRow = async () => {
    const cell = await screen.findByText('omer', { selector: 'td div' })
    return cell.closest('tr') as HTMLElement
  }

  it('clicking L on omer/Prod PUTs actions 3', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`/api/permissions/${OMER_ID}/${PROD_TAG_ID}`, async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ actions: 3 })
      }),
    )
    renderApp('/settings/users')
    expect(await screen.findByRole('heading', { level: 2, name: 'Users and permissions' })).toBeInTheDocument()
    const row = await omerRow()
    // Columns are Dev, Staging, Prod: Prod is the third cell.
    fireEvent.click(within(row).getAllByTitle('Logs')[2])
    await waitFor(() => expect(bodies).toEqual([{ actions: 3 }]))
  })

  it('choosing preset Operator PUTs 7', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`/api/permissions/${OMER_ID}/${PROD_TAG_ID}`, async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ actions: 7 })
      }),
    )
    renderApp('/settings/users')
    await omerRow()
    fireEvent.change(screen.getByLabelText('Preset for omer on Prod'), { target: { value: 'Operator' } })
    await waitFor(() => expect(bodies).toEqual([{ actions: 7 }]))
  })
})

describe('users', () => {
  it('Delete disabled for current user', async () => {
    renderApp('/settings/users')
    const you = await screen.findByText('(you)')
    const row = you.closest('tr') as HTMLElement
    expect(within(row).getByRole('button', { name: 'Delete' })).toBeDisabled()
    expect(within(row).getByRole('button', { name: 'Delete' })).toHaveAttribute('title', 'You cannot delete yourself')
    const other = screen.getAllByText('dana', { selector: 'td' })[0].closest('tr') as HTMLElement
    expect(within(other).getByRole('button', { name: 'Delete' })).toBeEnabled()
  })
})

describe('add user', () => {
  it('shows temporary password once', async () => {
    server.use(
      http.post('/api/users', async ({ request }) => {
        expect(await request.json()).toEqual({ username: 'alex', admin: false })
        return HttpResponse.json({ user: { ...USERS.items[2], id: 9, username: 'alex' }, temporary_password: 'abcd-1234-ef56' }, { status: 201 })
      }),
    )
    renderApp('/settings/users')
    fireEvent.click(await screen.findByRole('button', { name: 'Add user' }))
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'alex' } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Add user' }))
    expect(await screen.findByText('Temporary password for alex')).toBeInTheDocument()
    expect(screen.getByText('abcd-1234-ef56')).toBeInTheDocument()
    expect(screen.getByText('Shown once. Copy it now.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByText('abcd-1234-ef56')).not.toBeInTheDocument()
  })
})
