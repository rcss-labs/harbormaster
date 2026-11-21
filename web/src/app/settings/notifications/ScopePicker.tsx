import { useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import type { NotificationSelector, NotificationTargets, SelectorKind } from '../../../api/notifications'
import type { OffHoursTags } from '../../../api/offhours'
import {
  MAX_SCOPE_ENTRIES,
  MAX_THRESHOLD,
  SCOPE_GROUPS,
  addableAwsTagKey,
  scopeOptions,
  thresholdPhrase,
  type ScopeEntry,
  type ScopeKey,
  type ScopeOption,
} from './notifications'
import styles from './notifications.module.css'

interface ScopePickerProps {
  entries: ScopeEntry[]
  /** The saved route's entries: names for choices the lists no longer offer, and which of them are gone. */
  saved: NotificationSelector[]
  targets: NotificationTargets | undefined
  tags: OffHoursTags | undefined
  onAdd: (key: ScopeKey) => void
  onRemove: (key: ScopeKey) => void
  onThreshold: (key: ScopeKey, value: number) => void
}

const EMPTY_TARGETS: NotificationTargets = { accounts: [], clusters: [], services: [] }
const EMPTY_TAGS: OffHoursTags = { items: [], harbormaster: [] }

const KIND_OF_PREFIX: [string, SelectorKind][] = [
  ['account:', 'account'],
  ['cluster:', 'cluster'],
  ['service:', 'service'],
  ['hm:', 'harbormaster'],
  ['aws:', 'aws'],
]

const kindOf = (key: ScopeKey): SelectorKind => KIND_OF_PREFIX.find(([prefix]) => key.startsWith(prefix))?.[1] ?? 'aws'

/** Chips for the entries picked, and a combobox that adds one more (accounts, clusters, services, tags). */
export function ScopePicker({ entries, saved, targets, tags, onAdd, onRemove, onThreshold }: ScopePickerProps) {
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const input = useRef<HTMLInputElement>(null)
  const listId = useId()

  const options = useMemo(() => scopeOptions(targets ?? EMPTY_TARGETS, tags ?? EMPTY_TAGS, saved), [targets, tags, saved])
  const byKey = useMemo(() => new Map(options.map((option) => [option.key, option])), [options])
  const picked = useMemo(() => new Set(entries.map((entry) => entry.key)), [entries])

  const needle = query.trim().toLowerCase()
  const visible = useMemo(
    () => options.filter((option) => !picked.has(option.key) && `${option.label} ${option.sub}`.toLowerCase().includes(needle)),
    [options, picked, needle],
  )
  // A typed key=value that is not listed can be added as an AWS tag.
  const created = useMemo(() => {
    const key = addableAwsTagKey(query)
    if (!key || picked.has(key) || byKey.has(key)) return null
    return { key, label: key.slice('aws:'.length) }
  }, [query, picked, byKey])
  const choices: { key: ScopeKey; label: string; sub: string; group: string }[] = [
    ...visible.map((option) => ({ key: option.key, label: option.label, sub: option.sub, group: option.group })),
    ...(created ? [{ key: created.key, label: `Add ${created.label} as an AWS tag`, sub: '', group: 'AWS tags' }] : []),
  ]
  const full = entries.length >= MAX_SCOPE_ENTRIES

  const choose = (key: ScopeKey) => {
    onAdd(key)
    setQuery('')
    setActive(0)
    input.current?.focus()
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setOpen(true)
      setActive((index) => Math.min(choices.length - 1, index + 1))
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActive((index) => Math.max(0, index - 1))
    } else if (event.key === 'Enter') {
      if (open && choices[active]) {
        event.preventDefault()
        choose(choices[active].key)
      }
    } else if (event.key === 'Escape') {
      setOpen(false)
    }
  }

  let lastGroup = ''
  return (
    <div className={styles.field}>
      <div className={styles.chips} role="list" aria-label="Alerts from">
        {entries.length === 0 && <span className={styles.everything}>Everything</span>}
        {entries.map((entry) => (
          <Chip key={entry.key} entry={entry} option={byKey.get(entry.key)} onRemove={onRemove} onThreshold={onThreshold} />
        ))}
      </div>
      <div className={styles.combo}>
        <input
          ref={input}
          className={styles.ctl}
          role="combobox"
          aria-label="Add to scope"
          aria-expanded={open && choices.length > 0}
          aria-controls={listId}
          aria-autocomplete="list"
          autoComplete="off"
          placeholder={full ? `Up to ${MAX_SCOPE_ENTRIES} entries` : 'Search accounts, clusters, services and tags, or type key=value'}
          disabled={full}
          value={query}
          onChange={(event) => {
            setQuery(event.target.value)
            setActive(0)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 120)}
          onKeyDown={onKeyDown}
        />
        {open && choices.length > 0 && (
          <ul id={listId} role="listbox" aria-label="Scope choices" className={styles.listbox}>
            {choices.map((choice, index) => {
              const heading = choice.group !== lastGroup && SCOPE_GROUPS.includes(choice.group as (typeof SCOPE_GROUPS)[number])
              lastGroup = choice.group
              return (
                <li key={choice.key} role="presentation">
                  {heading && <div className={styles.groupHead}>{choice.group}</div>}
                  <div
                    role="option"
                    aria-selected={index === active}
                    className={styles.option}
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => choose(choice.key)}
                  >
                    <span>{choice.label}</span>
                    {choice.sub && <span className={styles.optionSub}>{choice.sub}</span>}
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </div>
      <p className={styles.help}>Services and tags send one notification for each failure. Clusters and accounts send one notification when the number of failing services reaches the number you set.</p>
      <p className={styles.help}>Leave empty for every failure everywhere.</p>
    </div>
  )
}

function Chip({
  entry,
  option,
  onRemove,
  onThreshold,
}: {
  entry: ScopeEntry
  option: ScopeOption | undefined
  onRemove: (key: ScopeKey) => void
  onThreshold: (key: ScopeKey, value: number) => void
}) {
  const kind = kindOf(entry.key)
  const label = option?.label ?? (entry.key.startsWith('aws:') ? entry.key.slice('aws:'.length) : entry.key)
  const missing = option?.missing === true
  const isGroup = entry.threshold > 0
  // The field keeps what is typed (it may be empty on the way to a new number); only a valid number is committed.
  const [text, setText] = useState(String(entry.threshold))
  const [seen, setSeen] = useState(entry.threshold)
  if (seen !== entry.threshold) {
    setSeen(entry.threshold)
    setText(String(entry.threshold))
  }
  return (
    <span className={`${styles.chip} ${missing ? styles.chipMissing : ''}`.trim()} role="listitem">
      <span className={styles.chipKind}>{kind === 'harbormaster' ? 'tag' : kind === 'aws' ? 'aws tag' : kind}</span>
      <span className={styles.chipLabel}>{missing ? `${label} · No longer exists` : label}</span>
      {isGroup && (
        <>
          <input
            type="number"
            className={styles.threshold}
            min={1}
            max={MAX_THRESHOLD}
            step={1}
            value={text}
            aria-label={`Failing services needed for ${label}`}
            onChange={(event) => {
              setText(event.target.value)
              const typed = event.target.valueAsNumber
              if (Number.isFinite(typed)) onThreshold(entry.key, typed)
            }}
            onBlur={() => setText(String(entry.threshold))}
          />
          <span className={styles.chipPhrase}>{thresholdPhrase(entry.threshold)}</span>
        </>
      )}
      <button type="button" className={styles.chipRemove} aria-label={`Remove ${label}`} onClick={() => onRemove(entry.key)}>
        ×
      </button>
    </span>
  )
}
