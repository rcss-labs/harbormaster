import { useEffect } from 'react'

/**
 * Mirrors `window.visualViewport` into `--hm-vvh` (visible height, px) and `--hm-vvt` (offsetTop, px) on the root element,
 * so phone layouts can end above the on-screen keyboard. Removes both on unmount; does nothing where the API is missing.
 */
export function useVisualViewport(): void {
  useEffect(() => {
    const viewport = window.visualViewport
    if (!viewport) return
    const root = document.documentElement
    const write = () => {
      root.style.setProperty('--hm-vvh', `${viewport.height}px`)
      root.style.setProperty('--hm-vvt', `${viewport.offsetTop}px`)
    }
    write()
    viewport.addEventListener('resize', write)
    viewport.addEventListener('scroll', write)
    return () => {
      viewport.removeEventListener('resize', write)
      viewport.removeEventListener('scroll', write)
      root.style.removeProperty('--hm-vvh')
      root.style.removeProperty('--hm-vvt')
    }
  }, [])
}
