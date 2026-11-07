export const isMac = () => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
export const shortcutLabel = () => (isMac() ? '⌘K' : 'Ctrl K')
