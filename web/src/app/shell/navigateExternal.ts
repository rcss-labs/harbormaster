/** Full browser navigation to a URL the server returned. Kept separate so tests can stub it. */
export function navigateExternal(url: string): void {
  window.location.assign(url)
}
