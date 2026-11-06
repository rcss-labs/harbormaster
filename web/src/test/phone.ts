import { PHONE_QUERY } from '../components/useNarrow'

/**
 * Stubs `window.matchMedia` so the phone query reports `matches` and every other query reports false.
 * Returns a restore function; call it in `afterEach`.
 */
export function installPhoneMedia(matches: boolean): () => void {
  const original = Object.getOwnPropertyDescriptor(window, 'matchMedia')
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (query: string): MediaQueryList => {
      const list = {
        matches: query === PHONE_QUERY && matches,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }
      return list as unknown as MediaQueryList
    },
  })
  return () => {
    if (original) Object.defineProperty(window, 'matchMedia', original)
    else Reflect.deleteProperty(window, 'matchMedia')
  }
}

/** jsdom has no ResizeObserver; this one never fires, which is enough for components that only observe. */
export function installResizeObserver(): () => void {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'ResizeObserver')
  class NoopResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Object.defineProperty(globalThis, 'ResizeObserver', { configurable: true, writable: true, value: NoopResizeObserver })
  return () => {
    if (original) Object.defineProperty(globalThis, 'ResizeObserver', original)
    else Reflect.deleteProperty(globalThis, 'ResizeObserver')
  }
}

export interface VisualViewportState {
  height: number
  offsetTop: number
}

/** Replaces `window.visualViewport` with a controllable stand-in. `set` updates the values and dispatches `resize`. */
export function installVisualViewport(initial: VisualViewportState): { set(next: Partial<VisualViewportState>): void; restore(): void } {
  const original = Object.getOwnPropertyDescriptor(window, 'visualViewport')
  const target = new EventTarget() as EventTarget & VisualViewportState
  target.height = initial.height
  target.offsetTop = initial.offsetTop
  Object.defineProperty(window, 'visualViewport', { configurable: true, writable: true, value: target })
  return {
    set(next) {
      Object.assign(target, next)
      target.dispatchEvent(new Event('resize'))
    },
    restore() {
      if (original) Object.defineProperty(window, 'visualViewport', original)
      else Reflect.deleteProperty(window, 'visualViewport')
    },
  }
}
