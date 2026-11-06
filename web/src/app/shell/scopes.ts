import type { Filters } from '../../api/hooks'
import type { MeDTO } from '../../api/types'
import { visibleTagIds } from './useFilters'

/** A named account / region / tag selection, stored per user in this browser. */
export interface SavedScope {
  name: string
  account?: string
  region?: string
  tag?: number[]
}

const storageKey = (user: string) => `harbormaster-scopes:${user}`

function parseScopes(raw: string | null): SavedScope[] {
  if (!raw) return []
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.flatMap((entry): SavedScope[] => {
      if (!entry || typeof entry !== 'object') return []
      const { name, account, region, tag } = entry as Record<string, unknown>
      if (typeof name !== 'string' || name.trim() === '') return []
      const tags = Array.isArray(tag) ? tag.filter((id): id is number => Number.isInteger(id)) : []
      return [
        {
          name,
          ...(typeof account === 'string' ? { account } : {}),
          ...(typeof region === 'string' ? { region } : {}),
          ...(tags.length > 0 ? { tag: tags } : {}),
        },
      ]
    })
  } catch {
    return []
  }
}

export function loadScopes(user: string): SavedScope[] {
  try {
    return parseScopes(localStorage.getItem(storageKey(user)))
  } catch {
    return []
  }
}

function writeScopes(user: string, scopes: SavedScope[]): SavedScope[] {
  try {
    localStorage.setItem(storageKey(user), JSON.stringify(scopes))
  } catch {
    // storage unavailable: the scope list lasts for this page view only
  }
  return scopes
}

/** Adds the scope, replacing one with the same name; returns the new list. */
export function saveScope(user: string, scope: SavedScope): SavedScope[] {
  const others = loadScopes(user).filter((existing) => existing.name !== scope.name)
  return writeScopes(user, [...others, scope])
}

export function removeScope(user: string, name: string): SavedScope[] {
  return writeScopes(
    user,
    loadScopes(user).filter((existing) => existing.name !== name),
  )
}

/**
 * The filters a saved scope stands for, for this user. A stored scope must never widen what the user sees, so tag ids
 * they no longer hold View on, and an account or region they no longer have, are dropped.
 */
export function applyScope(scope: SavedScope, me: MeDTO): Filters {
  const visible = new Set(visibleTagIds(me))
  const tags = [...new Set(scope.tag ?? [])].filter((id) => visible.has(id)).sort((a, b) => a - b)
  return {
    account: me.accounts.some((account) => account.account_id === scope.account) ? scope.account : undefined,
    region: me.regions.includes(scope.region ?? '') ? scope.region : undefined,
    tag: tags.length > 0 ? tags : undefined,
  }
}
