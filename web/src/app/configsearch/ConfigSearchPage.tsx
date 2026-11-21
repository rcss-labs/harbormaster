import { useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { useConfigSearch, type ConfigSearchItemDTO } from '../../api/configSearch'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { useToast } from '../../components/Toast'
import { usePhone } from '../../components/usePhone'
import { useCrumbs } from '../shell/crumbs'
import { useFilters } from '../shell/useFilters'
import { appendField, csvFilename, FIELDS, queryFromParams, resultCountLabel, selectionOf, toCSV } from './model'
import { ResultCards } from './ResultCards'
import { ResultsTable } from './ResultsTable'
import styles from './configsearch.module.css'

const EXAMPLES = ['image:node:18', 'env:DATABASE_URL', 'port:8080']
const PLACEHOLDER = 'image:node:18 env:DATABASE_URL port:8080'
const INTRO =
  'Search the task definitions of the services you can see. Environment variable and secret values are never searched or shown, only their names.'
const BASE_NOTE = 'Base image is the one set for the repository on the Security page. It is not checked against the image here.'
const MAX_ROWS_LABEL = '2,000'

/** Triggers a browser download of `text` as a CSV file. */
function download(text: string, filename: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.append(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

interface SearchFormProps {
  initial: string
  error: string | null
  onSubmit: (query: string) => void
}

/** The query box and field chips. Remounted (by key) when the URL's query changes, so it always starts from the URL. */
function SearchForm({ initial, error, onSubmit }: SearchFormProps) {
  const [text, setText] = useState(initial)
  const inputRef = useRef<HTMLInputElement>(null)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    onSubmit(text)
  }
  return (
    <form className={styles.form} aria-label="Config search" onSubmit={submit}>
      <div className={styles.queryRow}>
        <input
          ref={inputRef}
          className={styles.input}
          type="text"
          spellCheck={false}
          autoComplete="off"
          placeholder={PLACEHOLDER}
          aria-label="Config search query"
          aria-invalid={error !== null}
          value={text}
          onChange={(event) => setText(event.target.value)}
        />
        <button type="submit" className={`${styles.btn} ${styles.primary}`}>
          Search
        </button>
      </div>
      {error !== null && (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      )}
      <div className={styles.fieldChips} role="group" aria-label="Search fields">
        {FIELDS.map((field) => (
          <button
            key={field.field}
            type="button"
            className={styles.fieldChip}
            title={`${field.label}, for example ${field.example}`}
            onClick={() => {
              setText((current) => appendField(current, field.field))
              inputRef.current?.focus()
            }}
          >
            {field.field}:
          </button>
        ))}
      </div>
      <p className={styles.hint}>{INTRO}</p>
      <p className={styles.hint}>{BASE_NOTE}</p>
    </form>
  )
}

export function ConfigSearchPage() {
  useCrumbs([['Estate', '/'], ['Config search']])
  const [params, setParams] = useSearchParams()
  const phone = usePhone()
  const toast = useToast()
  const { filters } = useFilters()
  const urlQuery = queryFromParams(params)
  const result = useConfigSearch(urlQuery, filters)

  // The selection belongs to one query and one set of filters; a different scope starts empty.
  const scope = `${urlQuery}\u0000${JSON.stringify(filters)}`
  const [picked, setPicked] = useState<{ scope: string; ids: ReadonlySet<string> }>({ scope, ids: new Set() })
  const items: ConfigSearchItemDTO[] = result.data?.items ?? []
  const selected = new Set(picked.scope === scope ? [...picked.ids].filter((id) => items.some((item) => item.id === id)) : [])

  const setSelected = (ids: Set<string>) => setPicked({ scope, ids })
  const toggle = (id: string) => {
    const next = new Set(selected)
    if (!next.delete(id)) next.add(id)
    setSelected(next)
  }
  const toggleAll = () => setSelected(selected.size === items.length ? new Set() : new Set(items.map((item) => item.id)))

  const submit = (query: string) =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current)
        if (query.trim() === '') next.delete('q')
        else next.set('q', query.trim())
        return next
      },
      { replace: true },
    )

  // The ticked rows, or every row when none is ticked.
  const chosen = selected.size > 0 ? items.filter((item) => selected.has(item.id)) : items
  const selection = selectionOf(urlQuery, filters, chosen.map((item) => item.id))
  const exportCsv = () => download(toCSV(chosen), csvFilename(new Date()))
  const copyIds = async () => {
    try {
      await navigator.clipboard.writeText(selection.serviceIds.join('\n'))
      toast.show('Service IDs copied', 'ok')
    } catch {
      toast.show('Could not copy the service IDs.', 'bad')
    }
  }

  const head = <h1 className="hm-phone-title-sr">Config search</h1>
  const form = (
    <Panel title="Search">
      <SearchForm key={urlQuery} initial={urlQuery} error={result.isError ? result.error.message : null} onSubmit={submit} />
    </Panel>
  )

  let body
  if (urlQuery.trim() === '') {
    body = (
      <EmptyState>
        <p>Try one of these searches.</p>
        <div className={styles.examples}>
          {EXAMPLES.map((example) => (
            <button key={example} type="button" className={styles.btn} onClick={() => submit(example)}>
              {example}
            </button>
          ))}
        </div>
      </EmptyState>
    )
  } else if (result.isError) {
    body = null // the message is shown under the input
  } else if (result.isPending || result.isPlaceholderData) {
    // Placeholder data is the previous query's result; it must not be shown or exported under the new query.
    body = <Panel title="Results" loading>{null}</Panel>
  } else {
    const data = result.data
    body = (
      <>
        <div className={styles.resultsHead}>
          <span className={styles.count}>{resultCountLabel(data.total)}</span>
          <span className={styles.stats}>Searched {data.searched} services you can see.</span>
        </div>
        {data.truncated && (
          <p className={styles.notice}>
            Showing the first {MAX_ROWS_LABEL} of {data.total.toLocaleString('en-US')} matching services. Narrow the search to see the rest.
          </p>
        )}
        {data.unindexed > 0 && (
          <p className={styles.notice}>{data.unindexed} services have no task definition loaded yet and were not searched.</p>
        )}
        <div className={styles.actions}>
          {selected.size > 0 && <span className={styles.selected}>{selected.size} selected</span>}
          <button type="button" className={styles.btn} disabled={items.length === 0} onClick={exportCsv}>
            Export CSV
          </button>
          <button type="button" className={styles.btn} disabled={items.length === 0} onClick={() => void copyIds()}>
            Copy service IDs
          </button>
        </div>
        {items.length === 0 ? (
          <EmptyState>No services match.</EmptyState>
        ) : phone ? (
          <ResultCards items={items} selected={selected} onToggle={toggle} />
        ) : (
          <Panel title="Results" flush>
            <ResultsTable items={items} selected={selected} onToggle={toggle} onToggleAll={toggleAll} />
          </Panel>
        )}
      </>
    )
  }

  return (
    <div className={styles.page}>
      {head}
      {form}
      {body}
    </div>
  )
}
