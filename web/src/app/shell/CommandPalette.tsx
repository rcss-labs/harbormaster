import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useNavigate } from 'react-router'
import { useClusters, useSearch } from '../../api/hooks'
import { useAlerts } from '../../api/observe'
import type { SearchResultDTO } from '../../api/types'
import { useAuth } from '../auth/AuthProvider'
import { toggleTheme } from '../theme'
import { usePhone } from '../../components/usePhone'
import { useShellState } from './ShellState'
import { shortcutLabel } from './platform'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

const LIMIT_EMPTY = 4
const LIMIT_FILTERING = 6
const TASKS_MIN_QUERY = 3
const SEARCH_MIN_QUERY = 2

interface PaletteItem {
  key: string
  /** Short mono tag in front of the label. */
  tag: string
  label: string
  sub: string
  run: () => void
}

interface PaletteGroup {
  name: string
  items: PaletteItem[]
}

const matches = (query: string, ...parts: string[]) => parts.join(' ').toLowerCase().includes(query)

/** ⌘K / Ctrl+K command palette: jump to a service, cluster or task, or run a shell action. */
export function CommandPalette() {
  const { closePalette, toggleSidebar, toggleRail } = useShellState()
  const phone = usePhone()
  const navigate = useNavigate()
  const { me } = useAuth()
  const { filters } = useFilters()
  const [text, setText] = useState('')
  const [activeKey, setActiveKey] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const baseId = useId()
  const query = text.trim().toLowerCase()

  const alerts = useAlerts(filters)
  const clusters = useClusters(filters)
  const search = useSearch(query, filters)
  const searchItems = useMemo<SearchResultDTO[]>(
    () => (query.length >= SEARCH_MIN_QUERY ? (search.data?.items ?? []) : []),
    [query, search.data],
  )

  // Return focus to whatever had it before the palette opened.
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    inputRef.current?.focus()
    return () => previous?.focus()
  }, [])

  const groups = useMemo<PaletteGroup[]>(() => {
    const go = (href: string) => () => {
      closePalette()
      navigate(href)
    }
    const act = (fn: () => void) => () => {
      closePalette()
      fn()
    }
    const fromSearch = (kind: SearchResultDTO['kind'], tag: string): PaletteItem[] =>
      searchItems.filter((r) => r.kind === kind).map((r) => ({ key: `${kind}:${r.href}:${r.label}`, tag, label: r.label, sub: r.sub, run: go(r.href) }))
    const filtering = query !== ''
    const searching = query.length >= SEARCH_MIN_QUERY

    // Services: failing ones first when nothing is typed (the alerts are already worst first).
    const seen = new Set<string>()
    const problemServices: PaletteItem[] = (alerts.data?.items ?? []).flatMap((a) => {
      if (!a.service_id || seen.has(a.service_id)) return []
      seen.add(a.service_id)
      return [{ key: `service:${a.service_id}`, tag: 'SVC', label: a.service, sub: a.cluster, run: go(`/services/${a.service_id}/overview`) }]
    })
    const services = searching ? fromSearch('service', 'SVC') : problemServices.filter((s) => matches(query, s.label, s.sub))

    const clusterItems: PaletteItem[] = (clusters.data?.items ?? []).map((c) => ({
      key: `cluster:${c.id}`,
      tag: 'CL',
      label: c.name,
      sub: `${c.account_name} · ${c.region}`,
      run: go(`/clusters/${c.id}`),
    }))
    const clusterList = searching ? fromSearch('cluster', 'CL') : clusterItems.filter((c) => matches(query, c.label, c.sub))

    const tasks = query.length >= TASKS_MIN_QUERY ? fromSearch('task', 'TASK') : []

    const actions: PaletteItem[] = [
      { key: 'act:problem-tasks', tag: 'ACT', label: 'Show problem tasks', sub: 'Tasks', run: go('/tasks?filter=problems') },
      { key: 'act:theme', tag: 'ACT', label: 'Toggle theme', sub: 'Light / dark', run: act(() => void toggleTheme()) },
      { key: 'act:sidebar', tag: 'ACT', label: 'Toggle sidebar', sub: '[', run: act(toggleSidebar) },
      { key: 'act:activity', tag: 'ACT', label: 'Toggle activity', sub: 'Rail', run: act(toggleRail) },
    ].filter((a) => matches(query, a.label, a.sub) && !(phone && a.key === 'act:sidebar'))

    const pages: [string, string][] = [
      ['Overview', '/'],
      ['Services', '/services'],
      ['Clusters', '/clusters'],
      ['Tasks', '/tasks'],
      ['Networks', '/networks'],
      ['Storage', '/storage'],
      ['Analytics', '/analytics/delivery'],
      ['Log search', '/logs'],
      me?.admin ? ['Settings', '/settings/general'] : ['My access', '/me/access'],
    ]
    const goTo: PaletteItem[] = pages.map(([label, href]) => ({ key: `nav:${href}`, tag: 'NAV', label, sub: href, run: go(href) })).filter((p) => matches(query, p.label))

    const limit = filtering ? LIMIT_FILTERING : LIMIT_EMPTY
    return [
      { name: 'Actions', items: actions },
      { name: 'Services', items: services },
      { name: 'Clusters', items: clusterList },
      { name: 'Tasks', items: tasks },
      { name: 'Go to', items: goTo },
    ]
      .map((g) => ({ ...g, items: g.items.slice(0, limit) }))
      .filter((g) => g.items.length > 0)
  }, [query, searchItems, alerts.data, clusters.data, me, phone, closePalette, navigate, toggleSidebar, toggleRail])

  const flat = groups.flatMap((g) => g.items)
  const foundIndex = flat.findIndex((item) => item.key === activeKey)
  const activeIndex = foundIndex === -1 ? 0 : foundIndex
  const optionId = (index: number) => `${baseId}-opt-${index}`

  useEffect(() => {
    document.getElementById(optionId(activeIndex))?.scrollIntoView?.({ block: 'nearest' })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeIndex, flat.length])

  const move = (step: number) => {
    if (flat.length === 0) return
    setActiveKey(flat[(activeIndex + step + flat.length) % flat.length].key)
  }

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      closePalette()
    } else if (event.key === 'ArrowDown') {
      event.preventDefault()
      move(1)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      move(-1)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      flat[activeIndex]?.run()
    } else if (event.key === 'Tab') {
      // The input is the only stop; options are reached with the arrows.
      event.preventDefault()
      inputRef.current?.focus()
    }
  }

  const empty = flat.length === 0 && (query.length < SEARCH_MIN_QUERY || !search.isFetching)
  let index = -1
  return (
    <div className={styles.paletteBackdrop} onMouseDown={(event) => event.target === event.currentTarget && closePalette()}>
      <div className={styles.palette} role="dialog" aria-modal="true" aria-label="Command palette" onKeyDown={onKeyDown}>
        <div className={styles.paletteInput}>
          <span className={styles.paletteChevron} aria-hidden="true">
            ›
          </span>
          <input
            ref={inputRef}
            type="text"
            role="combobox"
            aria-label="Search or run a command"
            aria-expanded="true"
            aria-controls={`${baseId}-list`}
            aria-autocomplete="list"
            aria-activedescendant={flat.length > 0 ? optionId(activeIndex) : undefined}
            placeholder="Jump to a service, cluster or task, or type an action"
            autoComplete="off"
            spellCheck={false}
            value={text}
            onChange={(event) => setText(event.target.value)}
          />
          {phone ? (
            <button type="button" className={styles.paletteCancel} onClick={closePalette}>
              Cancel
            </button>
          ) : (
            <kbd className={styles.kbd}>esc</kbd>
          )}
        </div>
        <div className={styles.paletteList} role="listbox" id={`${baseId}-list`} aria-label="Results" ref={listRef}>
          {groups.map((group) => (
            <div key={group.name} role="group" aria-labelledby={`${baseId}-g-${group.name.replace(/\s/g, '-')}`}>
              <div className={styles.paletteGroup} id={`${baseId}-g-${group.name.replace(/\s/g, '-')}`}>
                {group.name}
              </div>
              {group.items.map((item) => {
                index += 1
                const i = index
                return (
                  <div
                    key={item.key}
                    id={optionId(i)}
                    role="option"
                    aria-selected={i === activeIndex}
                    className={`${styles.paletteOption} ${i === activeIndex ? styles.paletteActive : ''}`}
                    onMouseMove={() => i !== activeIndex && setActiveKey(item.key)}
                    onClick={item.run}
                  >
                    <span className={styles.paletteTag} aria-hidden="true">
                      {item.tag}
                    </span>
                    <span className={styles.paletteLabel}>{item.label}</span>
                    <span className={styles.paletteSub}>{item.sub}</span>
                  </div>
                )
              })}
            </div>
          ))}
          {empty && <div className={styles.paletteEmpty}>No match</div>}
        </div>
        <div className={styles.paletteFoot}>
          <span>↑↓ select</span>
          <span>↵ open</span>
          <span>{shortcutLabel()} toggle</span>
          <span className={styles.paletteFootEnd}>Actions respect your permissions</span>
        </div>
      </div>
    </div>
  )
}
