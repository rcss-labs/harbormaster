import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

const css = readFileSync(resolve(process.cwd(), 'src/app/settings/offhours/offhours.module.css'), 'utf8')

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 50))
})

describe('OffHoursPage on a phone', () => {
  it('grid scrolls inside its panel, page does not scroll sideways', async () => {
    renderApp('/settings/offhours')
    const cell = await screen.findByRole('button', { name: 'Mon 7:00 awake' })
    // The 24 columns keep a readable width, so the grid sits in its own horizontal scroller, never in the page.
    const scroller = cell.closest('.gridScroll')
    expect(scroller).not.toBeNull()
    expect(scroller!.closest('section')).not.toBeNull()
    expect(scroller!.parentElement).not.toBe(document.body)
    expect(within(scroller as HTMLElement).getAllByRole('button')).toHaveLength(168)
    // jsdom has no layout, so the rule that makes it scroll is read from the stylesheet itself.
    expect(css).toMatch(/\.gridScroll\s*{[^}]*overflow-x:\s*auto/)
  })

  it('services render as cards with the actions reachable', async () => {
    renderApp('/settings/offhours')
    const rows = await screen.findAllByRole('row')
    const web = rows.find((row) => within(row).queryByText('web', { exact: true }))
    expect(web).toBeDefined()
    expect(within(web!).getByText('Asleep until Mon 07:00')).toBeInTheDocument()
    expect(within(web!).getByRole('button', { name: 'Wake for 2 hours' })).toBeInTheDocument()
    const api = rows.find((row) => within(row).queryByText('api', { exact: true }))
    expect(within(api!).getByRole('button', { name: 'Put back to sleep' })).toBeInTheDocument()
    expect(screen.queryByRole('columnheader')).not.toBeInTheDocument()
  })

  it('editor fields stack in one column', async () => {
    renderApp('/settings/offhours')
    const name = await screen.findByLabelText('Name')
    const fields = name.closest('.fields')
    expect(fields).not.toBeNull()
    for (const label of ['Name', 'Services tagged', 'Timezone']) expect(fields).toContainElement(screen.getByLabelText(label))
    // The phone block of the stylesheet is the only place the single column is defined.
    const phoneBlock = css.slice(css.indexOf('@media (max-width: 700px)'))
    expect(phoneBlock).toMatch(/\.fields\s*{\s*grid-template-columns:\s*minmax\(0,\s*1fr\)/)
  })
})
