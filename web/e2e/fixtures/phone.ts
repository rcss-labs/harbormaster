import { expect, type Locator, type Page } from '@playwright/test'
import { saveScreenshot } from '../helpers'
import { ROUTES, type RouteSpec } from '../../e2e-floci/routes'

/**
 * Generic phone layout assertions, run in a real browser (jsdom has no layout, and checks that only looked at the DOM
 * let a sticky column cover names, a segmented control clip at the edge and labels wrap inside one-line boxes).
 *
 * `expectPhoneLayoutClean` reads everything in ONE `page.evaluate` (no box can go stale between reads) and fails with
 * one line per violation, each starting with its kind:
 *   overflow     content outside the screen width, or the document / main scrolls sideways
 *   clip         an overflow:hidden|clip box that cuts its content off
 *   target       a control under 44 px (36 px chips carry data-hm-hit="36" and a ::before at top -4px)
 *   input-font   an input, select or textarea under 16 px
 *   text-font    text under 12 px (SVG text is skipped: its CSS size is not its rendered size)
 *   wrap         a label wrapped onto a second line inside a group, toolbar, tab, main-nav item or [data-hm-oneline]
 *   row-bounds   the title of a [data-hm-row] outside the screen
 *   covered      something else (a sticky column, a bar) sits on top of a row's title
 *   bottom       the last element in main ends below the top of the tab bar, sticky bar or terminal keys row
 *   section-empty  a section[aria-label] in main with no text
 * Page errors, console errors and failing responses are collected from `watchPage` onwards.
 */

export interface PhoneOptions {
  /** CSS selectors whose elements (and descendants) may be clipped or overflow on purpose; `[data-hm-clip-ok]` always may. */
  allowClip?: string[]
}

/** Runs in the page. Self-contained: Playwright serialises it, so it may not use anything from this module. */
function collectViolations(options: { allowClip: string[] }): string[] {
  const out: string[] = []
  // The layout viewport: with content wider than the screen, mobile Chrome shrinks the page and innerWidth grows with it.
  const viewportWidth = document.documentElement.clientWidth
  const clipOkSelector = ['[data-hm-clip-ok]', ...options.allowClip].join(',')
  const reported = new Set<Element>()

  const describe = (el: Element): string => {
    const cls = typeof el.className === 'string' ? el.className.trim().split(/\s+/).filter(Boolean).slice(0, 2).join('.') : ''
    const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 30)
    return `<${el.tagName.toLowerCase()}${el.id ? `#${el.id}` : ''}${cls ? `.${cls}` : ''}>${text ? ` "${text}"` : ''}`
  }
  const report = (kind: string, el: Element, message: string) => out.push(`${kind}: ${describe(el)} ${message}`)
  const px = (value: number) => `${Math.round(value * 10) / 10}px`

  const isVisible = (el: Element): boolean => {
    const rect = el.getBoundingClientRect()
    if (rect.width <= 0 || rect.height <= 0) return false
    if (rect.width <= 1 && rect.height <= 1) return false // visually-hidden text
    if (el.closest('[aria-hidden="true"]')) return false
    const style = getComputedStyle(el)
    return style.visibility !== 'hidden' && style.display !== 'none'
  }
  const clipOk = (el: Element) => !!el.closest(clipOkSelector)
  const hasOwnText = (el: Element) => Array.from(el.childNodes).some((node) => node.nodeType === Node.TEXT_NODE && (node.textContent ?? '').trim() !== '')
  const nearestSidewaysScroller = (el: Element): Element | null => {
    for (let node = el.parentElement; node && node !== document.body && node !== document.documentElement; node = node.parentElement) {
      const overflowX = getComputedStyle(node).overflowX
      if ((overflowX === 'auto' || overflowX === 'scroll') && node.scrollWidth > node.clientWidth) return node
    }
    return null
  }
  // Number of text lines the node's text occupies (distinct tops of its client rects).
  const linesOf = (node: Text): number => {
    const range = document.createRange()
    range.selectNodeContents(node)
    const tops: number[] = []
    for (const rect of Array.from(range.getClientRects())) {
      if (rect.width <= 0 || rect.height <= 0) continue
      if (!tops.some((top) => Math.abs(top - rect.top) < rect.height / 2)) tops.push(rect.top)
    }
    return tops.length
  }

  // Document-level sideways scroll (same coordinate space under CSS zoom).
  const scrolling = document.scrollingElement
  if (scrolling && scrolling.scrollWidth > scrolling.clientWidth + 1) {
    out.push(`overflow: the document scrolls sideways (scrollWidth ${scrolling.scrollWidth} > clientWidth ${scrolling.clientWidth})`)
  }
  const main = document.querySelector('main')
  if (main && main.scrollWidth > main.clientWidth + 1) {
    report('overflow', main, `scrolls sideways (scrollWidth ${main.scrollWidth} > clientWidth ${main.clientWidth})`)
  }

  const targetSelector = 'button, input, select, textarea, [role=menuitem], [role=tab], [role=radio], [role=option], a[class*="btn"], a[data-hm-rl]'
  const oneLineSelector = '[role=radiogroup] button, [role=group] button, [role=toolbar] button, nav[aria-label="Main"] button, nav[aria-label="Main"] a, [role=tab], [data-hm-oneline]'
  const textInputs = (el: Element) => {
    if (el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) return true
    return el instanceof HTMLInputElement && !['hidden', 'checkbox', 'radio', 'range', 'color', 'file', 'button', 'submit', 'reset', 'image'].includes(el.type)
  }

  for (const el of Array.from(document.body.querySelectorAll('*'))) {
    if (el.closest('svg') && el.tagName.toLowerCase() !== 'svg') continue
    if (['script', 'style', 'template', 'noscript'].includes(el.tagName.toLowerCase())) continue
    if (!isVisible(el)) continue
    const rect = el.getBoundingClientRect()
    const style = getComputedStyle(el)

    // overflow: nothing outside the screen unless a sideways scroller (or an allowed clip) owns it.
    if ((rect.left < -1 || rect.right > viewportWidth + 1) && !clipOk(el) && !nearestSidewaysScroller(el) && !(el.parentElement && reported.has(el.parentElement))) {
      reported.add(el)
      report('overflow', el, `spans ${px(rect.left)}..${px(rect.right)} but the screen is 0..${viewportWidth}px`)
    }

    // clip: an overflow:hidden|clip box cutting its content off.
    if ((style.overflowX === 'hidden' || style.overflowX === 'clip') && el.scrollWidth > el.clientWidth + 1 && !clipOk(el)) {
      report('clip', el, `cuts content off (scrollWidth ${el.scrollWidth} > clientWidth ${el.clientWidth})`)
    }

    // target: 44 px controls; 36 px chips extend their hit area with ::before.
    if (el.matches(targetSelector)) {
      // A checkbox, radio or range input is judged by its label (a 22 px Toggle sits in a 44 px label); without one it is skipped.
      const nativeToggle = el instanceof HTMLInputElement && ['checkbox', 'radio', 'range'].includes(el.type)
      const label = nativeToggle ? el.closest('label') : null
      if (nativeToggle && !label) continue
      const box = (label ?? el).getBoundingClientRect()
      const hit36 = el.getAttribute('data-hm-hit') === '36'
      const min = hit36 ? 36 : 44
      const iconOnly = (el.textContent ?? '').trim() === ''
      if (box.height < min - 0.5 || (iconOnly && box.width < min - 0.5)) {
        report('target', el, `is ${px(box.width)} x ${px(box.height)}, needs ${iconOnly ? `${min} x ${min}` : `${min} tall`}`)
      } else if (hit36 && getComputedStyle(el, '::before').top !== '-4px') {
        report('target', el, 'carries data-hm-hit="36" but has no ::before extending the hit area to 44 (top -4px)')
      }
    }

    // fonts
    if (textInputs(el) && parseFloat(style.fontSize) < 16 - 0.01) report('input-font', el, `font-size is ${style.fontSize}, needs 16px`)
    if (hasOwnText(el) && parseFloat(style.fontSize) < 12 - 0.01) report('text-font', el, `font-size is ${style.fontSize}, needs 12px`)

    // wrap: a label that breaks onto a second line inside a one-line box.
    if (el.matches(oneLineSelector)) {
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        if ((node.textContent ?? '').trim() === '') continue
        const lines = linesOf(node as Text)
        if (lines > 1) {
          report('wrap', el, `label wraps onto ${lines} lines`)
          break
        }
      }
    }
  }

  // Row titles: inside the screen, and not covered by anything else.
  const savedScroll = { x: window.scrollX, y: window.scrollY, main: main ? main.scrollTop : 0 }
  for (const row of Array.from(document.querySelectorAll('[data-hm-row]')).slice(0, 40)) {
    if (!isVisible(row)) continue
    const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT, { acceptNode: (n) => ((n.textContent ?? '').trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT) })
    const title = walker.nextNode()?.parentElement
    if (!title) continue
    let rect = title.getBoundingClientRect()
    if (rect.left < -1 || rect.right > viewportWidth + 1) {
      report('row-bounds', title, `title spans ${px(rect.left)}..${px(rect.right)} but the screen is 0..${viewportWidth}px`)
      continue
    }
    row.scrollIntoView({ block: 'center', inline: 'nearest' })
    rect = title.getBoundingClientRect()
    const x = Math.min(Math.max(rect.left + rect.width / 2, 0), viewportWidth - 1)
    const y = Math.min(Math.max(rect.top + rect.height / 2, 0), window.innerHeight - 1)
    const top = document.elementFromPoint(x, y)
    if (top && top !== title && !title.contains(top) && !top.contains(title)) {
      report('covered', title, `title is covered by ${describe(top)}`)
    }
  }
  window.scrollTo(savedScroll.x, savedScroll.y)
  if (main) main.scrollTop = savedScroll.main

  // Sections with a label must say something.
  for (const section of Array.from(document.querySelectorAll('main section[aria-label]'))) {
    if ((section.textContent ?? '').trim() === '') report('section-empty', section, `section "${section.getAttribute('aria-label')}" has no text`)
  }

  // Bottom: scrolled to the end, nothing in main may sit under the tab bar, sticky bar or terminal keys row.
  if (main) {
    const bars = Array.from(document.querySelectorAll('nav[aria-label="Main"], body [data-hm-bar], .m-bar, [role=toolbar][aria-label="Terminal keys"]')).filter((bar) => isVisible(bar) && bar !== document.documentElement)
    if (bars.length > 0) {
      window.scrollTo(0, document.scrollingElement?.scrollHeight ?? 0)
      main.scrollTop = main.scrollHeight
      const barTop = Math.min(...bars.map((bar) => bar.getBoundingClientRect().top))
      let lowest: { el: Element; bottom: number } | null = null
      for (const el of Array.from(main.querySelectorAll('*'))) {
        if (el.closest('svg') && el.tagName.toLowerCase() !== 'svg') continue
        if (!isVisible(el)) continue
        const position = getComputedStyle(el).position
        if (position === 'fixed' || position === 'sticky') continue
        // Content of an inner scroller is the scroller's business; the scroller's own box is what counts.
        let insideScroller = false
        for (let node = el.parentElement; node && node !== main; node = node.parentElement) {
          const overflowY = getComputedStyle(node).overflowY
          if ((overflowY === 'auto' || overflowY === 'scroll') && node.scrollHeight > node.clientHeight + 1) insideScroller = true
        }
        if (insideScroller) continue
        const bottom = el.getBoundingClientRect().bottom
        if (!lowest || bottom > lowest.bottom) lowest = { el, bottom }
      }
      if (lowest && lowest.bottom > barTop + 1) {
        report('bottom', lowest.el, `ends at ${px(lowest.bottom)}, below the top of the bar at ${px(barTop)} (scrolled to the end)`)
      }
      window.scrollTo(savedScroll.x, savedScroll.y)
      main.scrollTop = savedScroll.main
    }
  }

  return out
}

/** The layout violations on the current page, one line each (empty when clean). */
export async function phoneViolations(page: Page, options: PhoneOptions = {}): Promise<string[]> {
  return page.evaluate(collectViolations, { allowClip: options.allowClip ?? [] })
}

type ExpectedFailure = (method: string, url: string, status: number) => boolean

const problemsByPage = new WeakMap<Page, string[]>()

/**
 * Starts collecting page errors, console errors and failing responses (call before `goto`). They are asserted by
 * every later `expectPhoneLayoutClean` on this page. `isExpectedFailure` is the allowlist for HTTP failures.
 */
// The app asks who is signed in on every load, so a signed-out page always sees this 401.
const signedOutProbe: ExpectedFailure = (method, url, status) => method === 'GET' && status === 401 && new URL(url).pathname === '/api/me'

export function watchPage(page: Page, isExpectedFailure: ExpectedFailure = signedOutProbe): string[] {
  const problems: string[] = []
  problemsByPage.set(page, problems)
  page.on('pageerror', (error) => problems.push(`pageerror: ${error.message}`))
  page.on('console', (message) => {
    if (message.type() !== 'error') return
    const text = message.text()
    // A failing HTTP response is judged by the allowlist below, not as a console error as well.
    if (text.startsWith('Failed to load resource')) return
    // Live tail and ECS Exec WebSockets cannot be served by the fake or by floci.
    if (/^WebSocket connection to .* failed/.test(text) && /\/(logs\/tail|exec)\b/.test(text)) return
    problems.push(`console error: ${text}`)
  })
  page.on('response', (response) => {
    if (response.status() >= 400 && !isExpectedFailure(response.request().method(), response.url(), response.status())) {
      problems.push(`response: ${response.request().method()} ${new URL(response.url()).pathname} -> ${response.status()}`)
    }
  })
  return problems
}

/** Fails with one line per layout violation (and per problem collected since `watchPage`). */
export async function expectPhoneLayoutClean(page: Page, options: PhoneOptions = {}) {
  expect(problemsByPage.get(page) ?? [], 'page errors, console errors and failing responses').toEqual([])
  expect(await phoneViolations(page, options), 'phone layout violations').toEqual([])
}

const SHEET_SELECTOR = '[role=dialog][aria-modal="true"], [role=menu]'

/** The open sheet's rect lies inside the viewport and its footer buttons are visible. Returns the violations. */
export async function sheetViolations(page: Page): Promise<string[]> {
  return page.evaluate((selector) => {
    const sheets = Array.from(document.querySelectorAll(selector)).filter((el) => {
      const rect = el.getBoundingClientRect()
      return rect.width > 0 && rect.height > 0
    })
    const sheet = sheets[sheets.length - 1]
    if (!sheet) return ['sheet: no open sheet found']
    const out: string[] = []
    const rect = sheet.getBoundingClientRect()
    const inside = (r: DOMRect) => r.left >= -1 && r.top >= -1 && r.right <= document.documentElement.clientWidth + 1 && r.bottom <= window.innerHeight + 1
    if (!inside(rect)) {
      out.push(`sheet: spans ${Math.round(rect.left)},${Math.round(rect.top)}..${Math.round(rect.right)},${Math.round(rect.bottom)} but the viewport is ${document.documentElement.clientWidth} x ${window.innerHeight}`)
    }
    for (const button of Array.from(sheet.querySelectorAll('footer button, [data-hm-sheet-footer] button, [class*="foot"] button'))) {
      const box = button.getBoundingClientRect()
      if (box.width <= 0 || box.height <= 0 || !inside(box)) {
        out.push(`sheet: footer button "${(button.textContent ?? '').trim()}" is not visible inside the viewport`)
      }
    }
    return out
  }, SHEET_SELECTOR)
}

export async function expectSheetWithinViewport(page: Page) {
  expect(await sheetViolations(page), 'sheet violations').toEqual([])
}

/** Opens the first "Actions for …" / "More actions for …" button's sheet; null when the page has none. */
export async function openFirstActionSheet(page: Page): Promise<Locator | null> {
  const trigger = page.getByRole('button', { name: /^(More actions|Actions) for / }).first()
  if ((await trigger.count()) === 0) return null
  await trigger.click()
  const sheet = page.locator(SHEET_SELECTOR).last()
  await expect(sheet).toBeVisible()
  return sheet
}

/** Opens the first action sheet, runs `fn` with it, closes it with Escape. Returns false when there was none. */
export async function withSheetOpen(page: Page, fn: (sheet: Locator) => Promise<void>): Promise<boolean> {
  const sheet = await openFirstActionSheet(page)
  if (!sheet) return false
  await fn(sheet)
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  return true
}

/** The floci route list plus the phone-only pages; each route's `path` takes the estate's targets. `/login` is visited signed out. */
export function PHONE_ROUTES(): RouteSpec[] {
  return [
    ...ROUTES,
    { name: 'more', path: () => '/more' },
    { name: 'password', path: () => '/password' },
    { name: 'login', path: () => '/login' },
  ]
}

export const PHONE_FIXME_REASON = 'wave 0: phone layer not merged'

/**
 * Route names whose phone pass is marked fixme until the page's lane lands. Lanes don't edit this list: they run their
 * routes with HM_PHONE_UNFIXME=1, and the lead empties it at the wave 1 gate.
 */
const PHONE_READY = new Set(['more', 'login'])
export const PHONE_FIXME: string[] = PHONE_ROUTES().map((route) => route.name).filter((name) => !PHONE_READY.has(name))

export function phoneFixme(routeName: string): boolean {
  return !process.env.HM_PHONE_UNFIXME && PHONE_FIXME.includes(routeName)
}

export interface PhonePassOptions {
  routeName: string
  path: string
  theme: 'dark' | 'light'
  isExpectedFailure?: ExpectedFailure
  /** What proves the page rendered; defaults to the main landmark. */
  ready?: Locator
  allowClip?: string[]
}

/**
 * The generic phone pass for one route and theme: load, wait for the page to settle, assert the layout, then open the
 * first row action sheet (when the page has one) and assert the sheet and the layout again. Screenshots go to
 * HM_E2E_SCREENSHOT_DIR as phone-<route>-<theme>.png and phone-<route>-<theme>-sheet.png when it is set.
 */
export async function runPhonePass(page: Page, options: PhonePassOptions) {
  await page.addInitScript((value) => localStorage.setItem('harbormaster-theme', value), options.theme)
  watchPage(page, options.isExpectedFailure)
  await page.goto(options.path)
  await expect(options.ready ?? page.getByRole('main')).toBeVisible()
  await page.waitForLoadState('networkidle')
  await saveScreenshot(page, `phone-${options.routeName}-${options.theme}.png`)
  await expectPhoneLayoutClean(page, { allowClip: options.allowClip })

  await withSheetOpen(page, async () => {
    await expectSheetWithinViewport(page)
    await saveScreenshot(page, `phone-${options.routeName}-${options.theme}-sheet.png`)
    await expectPhoneLayoutClean(page, { allowClip: options.allowClip })
  })
}
