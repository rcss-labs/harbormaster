import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'

/** [label, href]; the last part is the current page and has no link. */
export type CrumbPart = [label: string, href?: string]

interface CrumbsApi {
  crumbs: CrumbPart[]
  setCrumbs: (parts: CrumbPart[]) => void
}

const CrumbsContext = createContext<CrumbsApi | null>(null)

export function CrumbsProvider({ children }: { children: ReactNode }) {
  const [crumbs, setCrumbs] = useState<CrumbPart[]>([])
  const value = useMemo(() => ({ crumbs, setCrumbs }), [crumbs])
  return <CrumbsContext.Provider value={value}>{children}</CrumbsContext.Provider>
}

export function useCrumbsValue(): CrumbPart[] {
  return useContext(CrumbsContext)?.crumbs ?? []
}

/** Screens call this to set the top-bar breadcrumbs; they are cleared when the screen unmounts. */
export function useCrumbs(parts: CrumbPart[]): void {
  const setCrumbs = useContext(CrumbsContext)?.setCrumbs
  // Callers pass a fresh array every render, so depend on its content.
  const key = JSON.stringify(parts)
  useEffect(() => {
    if (!setCrumbs) return
    setCrumbs(JSON.parse(key) as CrumbPart[])
    return () => setCrumbs([])
  }, [key, setCrumbs])
}
