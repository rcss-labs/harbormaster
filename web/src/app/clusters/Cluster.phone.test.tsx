import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { CLUSTER_CORE } from '../../test/fixtures'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
let scrolled: HTMLElement[]

beforeEach(() => {
  localStorage.clear()
  restoreMedia = installPhoneMedia(true)
  // jsdom has no layout or scrolling; the section chips are checked by the element they scroll to.
  scrolled = []
  Element.prototype.scrollIntoView = function scrollIntoView(this: HTMLElement) {
    scrolled.push(this)
  }
})

afterEach(async () => {
  restoreMedia()
  Reflect.deleteProperty(Element.prototype, 'scrollIntoView')
  await new Promise((resolve) => setTimeout(resolve, 100))
})

describe('Cluster detail on phones', () => {
  it('section chips scroll to the services, tasks and capacity sections', async () => {
    const user = userEvent.setup()
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const sections = await screen.findByRole('group', { name: 'Section' })
    const expected: Array<[string, string]> = [
      ['Services', 'Services'],
      ['Tasks', 'Tasks by service'],
      ['Capacity', 'Cluster CPU and memory'],
    ]
    for (const [chip, sectionName] of expected) {
      scrolled = []
      await user.click(within(sections).getByRole('button', { name: chip }))
      expect(scrolled).toHaveLength(1)
      expect(scrolled[0].querySelector('h2')).toHaveTextContent(sectionName)
    }
  })

  it('the status row keeps the status, tag and Pin, and More actions lists Edit tag, Tasks and AWS console', async () => {
    const user = userEvent.setup()
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    await screen.findByRole('heading', { name: CLUSTER_CORE.name, level: 1 })
    expect(screen.getByRole('button', { name: `Pin ${CLUSTER_CORE.name}` })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: `More actions for ${CLUSTER_CORE.name}` }))
    const menu = screen.getByRole('menu', { name: `Actions for ${CLUSTER_CORE.name}` })
    const items = within(menu).getAllByRole('menuitem')
    expect(items.map((item) => item.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining('Edit tag'), expect.stringContaining('Tasks in this cluster'), expect.stringContaining('Open in AWS console')]),
    )
  })

  it('a service row sheet says where its tag comes from, in text', async () => {
    const user = userEvent.setup()
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const [opener] = await screen.findAllByRole('button', { name: /^Actions for / })
    await user.click(opener)
    const menu = screen.getByRole('menu', { name: opener.getAttribute('aria-label') ?? '' })
    expect(within(menu).getByText(new RegExp(`^Tag .+, inherited from cluster ${CLUSTER_CORE.name}$`))).toBeInTheDocument()
  })
})
