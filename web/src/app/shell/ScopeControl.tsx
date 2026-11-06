import { useState, type FormEvent } from 'react'
import type { MeDTO } from '../../api/types'
import { Icon } from '../../components/Icon'
import { Popover } from '../../components/Popover'
import { useAuth } from '../auth/AuthProvider'
import { Filters } from './Filters'
import { applyScope, loadScopes, removeScope, saveScope, type SavedScope } from './scopes'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

type ScopeFilters = ReturnType<typeof useFilters>['filters']

/** "<account> · <regions> · <tags>", the one-line summary on the scope button. */
export function scopeLabel(me: MeDTO, filters: ScopeFilters): string {
  const account = me.accounts.find((a) => a.account_id === filters.account)?.name ?? 'All accounts'
  const regions = filters.region ?? `${me.regions.length} ${me.regions.length === 1 ? 'region' : 'regions'}`
  const tagNames = (filters.tag ?? []).flatMap((id) => me.tags.find((tag) => tag.id === id)?.name ?? [])
  return `${account} · ${regions} · ${tagNames.length > 0 ? tagNames.join(', ') : 'All tags'}`
}

/** Only the parts of the scope that are narrowed, joined by " · "; "All" when nothing is. For the phone chip. */
export function scopeShort(me: MeDTO, filters: ScopeFilters): string {
  const parts: string[] = []
  if (filters.account) parts.push(me.accounts.find((a) => a.account_id === filters.account)?.name ?? filters.account)
  if (filters.region) parts.push(filters.region)
  const tagNames = (filters.tag ?? []).flatMap((id) => me.tags.find((tag) => tag.id === id)?.name ?? [])
  if (tagNames.length > 0) parts.push(tagNames.join(', '))
  return parts.length > 0 ? parts.join(' · ') : 'All'
}

/** The three filters and the saved scopes. Shared by the desktop popover and the phone sheet; its state ends when it unmounts. */
export function ScopeBody() {
  const { me } = useAuth()
  const { filters, setAccount, setRegion, setTags } = useFilters()
  const [naming, setNaming] = useState(false)
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<SavedScope[] | null>(null)
  if (!me) return null
  const username = me.username
  const saved = scopes ?? loadScopes(username)

  const apply = (scope: SavedScope) => {
    const next = applyScope(scope, me)
    setAccount(next.account)
    setRegion(next.region)
    setTags(next.tag ?? [])
  }
  const save = (event: FormEvent) => {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) return
    setScopes(saveScope(username, { name: trimmed, account: filters.account, region: filters.region, tag: filters.tag?.length ? filters.tag : undefined }))
    setNaming(false)
    setName('')
  }

  return (
    <div className={styles.scopePop}>
      <div className={styles.scopeFields}>
        <Filters />
      </div>
      <div className={styles.scopeSaved}>
        <strong>Saved scopes</strong>
        {saved.length === 0 && <span className={styles.freshCurrent}>None yet.</span>}
        <div className={styles.chips}>
          {saved.map((scope) => (
            <span key={scope.name} className={styles.chip}>
              <button type="button" className={styles.chipMain} onClick={() => apply(scope)}>
                {scope.name}
              </button>
              <button type="button" className={styles.chipX} aria-label={`Remove scope ${scope.name}`} onClick={() => setScopes(removeScope(username, scope.name))}>
                <Icon name="x" size={12} />
              </button>
            </span>
          ))}
        </div>
        {naming ? (
          <form className={styles.scopeName} onSubmit={save}>
            <input aria-label="Scope name" placeholder="Name this scope" value={name} onChange={(event) => setName(event.target.value)} autoFocus />
            <button type="submit" className={styles.btn} disabled={name.trim() === ''}>
              Save
            </button>
            <button type="button" className={styles.btn} onClick={() => setNaming(false)}>
              Cancel
            </button>
          </form>
        ) : (
          <button type="button" className={styles.btn} onClick={() => setNaming(true)}>
            Save current
          </button>
        )}
      </div>
    </div>
  )
}

/** One button for the account / region / tag selection; the three controls and the saved scopes live in its popover. */
export function ScopeControl() {
  const { me } = useAuth()
  const { filters } = useFilters()
  const [open, setOpen] = useState(false)
  if (!me) return null
  const label = scopeLabel(me, filters)

  return (
    <div className={styles.popAnchor}>
      <button type="button" className={styles.scopeBtn} aria-label={`Scope: ${label}`} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen((shown) => !shown)}>
        <Icon name="filter" />
        <span className={styles.scopeText}>{label}</span>
        <Icon name="chevd" />
      </button>
      <Popover open={open} onClose={() => setOpen(false)} label="Scope" width={360}>
        <ScopeBody />
      </Popover>
    </div>
  )
}
