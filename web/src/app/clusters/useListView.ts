import { useState } from 'react'

export type ListView = 'cards' | 'table'

export const LIST_VIEW_OPTIONS: { value: ListView; label: string }[] = [
  { value: 'cards', label: 'Cards' },
  { value: 'table', label: 'Table' },
]

const STORAGE_KEY = 'harbormaster-list-view'

function readStoredView(): ListView | null {
  try {
    const value = localStorage.getItem(STORAGE_KEY)
    return value === 'cards' || value === 'table' ? value : null
  } catch {
    return null
  }
}

/**
 * The view the user chose (remembered in localStorage once they toggle it), or `defaultView` until they choose.
 */
export function useListView(defaultView: ListView): [ListView, (view: ListView) => void] {
  const [chosenView, setChosenView] = useState<ListView | null>(readStoredView)

  const setView = (view: ListView) => {
    setChosenView(view)
    try {
      localStorage.setItem(STORAGE_KEY, view)
    } catch {
      // storage unavailable: the choice still applies for this visit
    }
  }

  return [chosenView ?? defaultView, setView]
}
