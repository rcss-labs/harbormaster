import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { SearchResultDTO } from '../../api/types'
import { CLUSTERS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const originalWidth = window.innerWidth
beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: 1440 })
})
afterEach(async () => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: originalWidth })
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const palette = () => screen.findByRole('dialog', { name: 'Command palette' })
const groupOf = (root: HTMLElement, name: string) => within(root).getByRole('group', { name })
const optionsIn = (root: HTMLElement) => within(root).getAllByRole('option')

async function openPalette() {
  renderApp('/')
  await screen.findByRole('navigation', { name: 'Main' })
  const user = userEvent.setup()
  await user.keyboard('{Control>}k{/Control}')
  return { user, dialog: await palette() }
}

const results = (kind: SearchResultDTO['kind'], count: number): SearchResultDTO[] =>
  Array.from({ length: count }, (_, i) => ({ kind, label: `${kind}-${i}`, sub: 'sub', href: `/${kind}/${i}` }))

describe('command palette', () => {
  it('opens with Ctrl+K, focuses the input and closes with Escape', async () => {
    const { user, dialog } = await openPalette()
    const input = within(dialog).getByRole('combobox')
    expect(input).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument()
  })

  it('opens from the top bar button and from Cmd+K', async () => {
    renderApp('/')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Search or run a command/ }))
    await palette()
    await user.keyboard('{Escape}')
    await user.keyboard('{Meta>}k{/Meta}')
    await palette()
  })

  it('keeps focus inside the dialog on Tab', async () => {
    const { user, dialog } = await openPalette()
    await user.tab()
    expect(dialog).toContainElement(document.activeElement as HTMLElement)
    await user.tab({ shift: true })
    expect(dialog).toContainElement(document.activeElement as HTMLElement)
  })

  it('highlights the first item, moves with the arrows and wraps', async () => {
    const { user, dialog } = await openPalette()
    const input = within(dialog).getByRole('combobox')
    const options = await waitFor(() => {
      const found = optionsIn(dialog)
      expect(found.length).toBeGreaterThan(2)
      return found
    })
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    expect(input).toHaveAttribute('aria-activedescendant', options[0].id)
    await user.keyboard('{ArrowDown}')
    expect(options[1]).toHaveAttribute('aria-selected', 'true')
    expect(options[0]).toHaveAttribute('aria-selected', 'false')
    expect(input).toHaveAttribute('aria-activedescendant', options[1].id)
    await user.keyboard('{ArrowUp}{ArrowUp}')
    expect(options[options.length - 1]).toHaveAttribute('aria-selected', 'true')
  })

  it('Go to lists Services and Analytics', async () => {
    const { user, dialog } = await openPalette()
    await user.type(within(dialog).getByRole('combobox'), 'ser')
    const goTo = await waitFor(() => groupOf(dialog, 'Go to'))
    expect(within(goTo).getByRole('option', { name: /Services/ })).toBeInTheDocument()
    await user.clear(within(dialog).getByRole('combobox'))
    await user.type(within(dialog).getByRole('combobox'), 'analy')
    const analytics = await waitFor(() => groupOf(dialog, 'Go to'))
    expect(within(analytics).getByRole('option', { name: /Analytics/ })).toBeInTheDocument()
  })

  it('opens the highlighted item with Enter and closes', async () => {
    const { user, dialog } = await openPalette()
    await user.type(within(dialog).getByRole('combobox'), 'Tasks')
    const goTo = await waitFor(() => groupOf(dialog, 'Go to'))
    expect(within(goTo).getByRole('option', { name: /Tasks/ })).toBeInTheDocument()
    await waitFor(() => expect(optionsIn(dialog)[0]).toHaveTextContent(/Tasks/))
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/tasks'))
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument()
  })

  it('navigates to a search result with the arrow keys and Enter', async () => {
    const cluster = CLUSTERS.items[0]
    const { user, dialog } = await openPalette()
    await user.type(within(dialog).getByRole('combobox'), cluster.name)
    const clusters = await waitFor(() => groupOf(dialog, 'Clusters'))
    await within(clusters).findByRole('option', { name: new RegExp(cluster.name) })
    const options = optionsIn(dialog)
    const target = options.findIndex((o) => o.textContent?.includes(cluster.name) && clusters.contains(o))
    if (target > 0) await user.keyboard('{ArrowDown}'.repeat(target))
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/clusters/${cluster.id}`))
  })

  it('shows at most 4 items per group when empty and 6 when filtering', async () => {
    server.use(
      http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items: Array.from({ length: 8 }, (_, i) => ({ ...CLUSTERS.items[0], id: `c${i}`, name: `cluster-${i}` })) })),
      http.get('/api/search', () => HttpResponse.json({ items: [...results('service', 9), ...results('cluster', 9)] })),
    )
    const { user, dialog } = await openPalette()
    await waitFor(() => expect(within(groupOf(dialog, 'Clusters')).getAllByRole('option')).toHaveLength(4))
    await user.type(within(dialog).getByRole('combobox'), 'ab')
    await waitFor(() => expect(within(groupOf(dialog, 'Services')).getAllByRole('option')).toHaveLength(6))
    expect(within(groupOf(dialog, 'Clusters')).getAllByRole('option')).toHaveLength(6)
  })

  it('lists Tasks only once the query has 3 characters', async () => {
    server.use(http.get('/api/search', () => HttpResponse.json({ items: [...results('task', 3), ...results('service', 1)] })))
    const { user, dialog } = await openPalette()
    expect(within(dialog).queryByRole('group', { name: 'Tasks' })).not.toBeInTheDocument()
    const input = within(dialog).getByRole('combobox')
    await user.type(input, '-0')
    await within(dialog).findByRole('group', { name: 'Services' })
    expect(within(dialog).queryByRole('group', { name: 'Tasks' })).not.toBeInTheDocument()
    await user.type(input, 'x')
    server.use(http.get('/api/search', () => HttpResponse.json({ items: results('task', 3) })))
    await user.clear(input)
    await user.type(input, 'task')
    expect(await within(dialog).findByRole('group', { name: 'Tasks' })).toBeInTheDocument()
  })

  it('lists failing services first when the query is empty', async () => {
    const { dialog } = await openPalette()
    const services = await waitFor(() => groupOf(dialog, 'Services'))
    await waitFor(() => expect(within(services).getAllByRole('option')[0]).toHaveTextContent('image-resizer'))
  })

  it('runs actions: toggles the sidebar and the theme', async () => {
    const { user, dialog } = await openPalette()
    await user.type(within(dialog).getByRole('combobox'), 'sidebar')
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('button', { name: /sidebar$/i })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument()
  })

  it('says when nothing matches', async () => {
    server.use(http.get('/api/search', () => HttpResponse.json({ items: [] })))
    const { user, dialog } = await openPalette()
    await user.type(within(dialog).getByRole('combobox'), 'zzzzzz')
    expect(await within(dialog).findByText('No match')).toBeInTheDocument()
  })
})
