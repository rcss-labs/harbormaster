import { screen, within } from '@testing-library/react'
import type { UserEvent } from '@testing-library/user-event'

/** Opens the scope popover (if it is not open) and returns it. */
export async function openScope(user: UserEvent): Promise<HTMLElement> {
  const existing = screen.queryByRole('dialog', { name: 'Scope' })
  if (existing) return existing
  await user.click(await screen.findByRole('button', { name: /^Scope:/ }))
  return screen.findByRole('dialog', { name: 'Scope' })
}

/** Opens the account menu at the foot of the sidebar (if it is not open) and returns it. */
export async function openAccountMenu(user: UserEvent): Promise<HTMLElement> {
  const existing = screen.queryByRole('group', { name: 'Account' })
  if (existing) return existing
  await user.click(await screen.findByRole('button', { name: /^Account menu for/ }))
  return screen.findByRole('group', { name: 'Account' })
}

export const inScope = async (user: UserEvent) => within(await openScope(user))
