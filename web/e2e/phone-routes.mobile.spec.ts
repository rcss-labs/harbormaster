import type { APIRequestContext, Page } from '@playwright/test'
import { PHONE_FIXME_REASON, PHONE_ROUTES, expectPhoneLayoutClean, phoneFixme, phoneViolations, runPhonePass, watchPage } from './fixtures/phone'
import { SVC, clusterUrl } from './fixtures/ids'
import { asUser, expect, test } from './fixtures/test'
import { serviceId } from './helpers'
import type { Targets } from '../e2e-floci/targets'

// The generic phone pass over every route of the fake estate, at 390 x 844 (chromium-mobile). Mutating phone specs run
// after chromium-flows, so this one only reads. Routes in PHONE_FIXME are skipped until the phone layer is merged.

const SEARCH_INDEXER = ['482109375512', 'eu-west-1', 'eu-apps', 'search-indexer'] as const
const ADMIN_ROUTE = /^settings-/
const THEMES = ['dark', 'light'] as const

/** Ids of the fake estate (e2e/fixtures/ids.ts); the standalone task has no fixed id, so it comes from the API. */
async function fakeTargets(request: APIRequestContext, user: 'sam' | 'omer'): Promise<Targets> {
  // omer only sees Dev and Staging, so his pass uses the Dev service he can operate.
  const service: readonly [string, string, string, string] = user === 'omer' ? SEARCH_INDEXER : SVC.paymentsApi
  const tasks = ((await (await request.get('/api/tasks')).json()).items ?? []) as { short_id: string; service: unknown }[]
  return {
    clusterId: clusterUrl(service[0], service[1], service[2]).replace('/clusters/', ''),
    serviceId: serviceId(...service),
    standaloneTaskShortId: tasks.find((task) => task.service === null)?.short_id ?? null,
    hasServicesApi: true,
  }
}

for (const user of ['sam', 'omer'] as const) {
  test.describe(`phone pass as ${user}`, () => {
    test.use(asUser(user))
    for (const route of PHONE_ROUTES().filter((r) => r.name !== 'login')) {
      for (const theme of THEMES) {
        test(`${route.name} ${theme}`, async ({ page, request }) => {
          test.fixme(phoneFixme(route.name), PHONE_FIXME_REASON)
          test.skip(user === 'omer' && ADMIN_ROUTE.test(route.name), 'admin pages are not for omer')
          const path = route.path(await fakeTargets(request, user))
          test.skip(path === null, `${route.name} is not available on this estate`)
          await runPhonePass(page, { routeName: `${user}-${route.name}`, path: path!, theme })
        })
      }
    }
  })
}

test.describe('phone pass signed out', () => {
  test.use({ storageState: { cookies: [], origins: [] } })
  for (const theme of THEMES) {
    test(`login ${theme}`, async ({ page }) => {
      test.fixme(phoneFixme('login'), PHONE_FIXME_REASON)
      await runPhonePass(page, { routeName: 'login', path: '/login', theme, ready: page.getByRole('button', { name: /Sign in/ }).first() })
    })
  }
})

// ---------------------------------------------------------------------------------------------------------------
// Self-test: every assertion is shown to fire on a planted defect (and to stay quiet on a clean page) before any lane
// relies on it. The pages are built with setContent at 390 wide; the viewport meta makes the layout viewport match.
// ---------------------------------------------------------------------------------------------------------------
test.describe('phone harness self-test', () => {
  const html = (body: string, css = '') =>
    `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><style>
      body { margin: 0; font: 16px/20px sans-serif }
      button, input { font: inherit }
      ${css}
    </style></head><body>${body}</body></html>`

  // Uses the violation list rather than the asserting wrapper so each planted defect can be matched by its kind.
  async function violationsOf(page: Page, content: string, allowClip?: string[]) {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.setContent(content)
    return phoneViolations(page, { allowClip })
  }
  const kinds = (violations: string[]) => violations.map((line) => line.split(':')[0])

  const NAV = '<nav aria-label="Main" style="position:fixed;left:0;right:0;bottom:0;height:56px;background:#ccc"><button style="min-height:44px;min-width:44px">Home</button></nav>'
  const CLEAN = html(
    `<main style="padding-bottom:72px">
      <section aria-label="Services"><p>Services</p></section>
      <div role="radiogroup"><button style="min-height:44px;min-width:60px;white-space:nowrap">All</button><button style="min-height:44px;white-space:nowrap">Mine</button></div>
      <div data-hm-row><span>payments-api</span></div>
      <input aria-label="Filter" style="font-size:16px;height:44px">
      <button data-hm-hit="36" class="chip" style="height:36px;min-width:44px">Chip</button>
    </main>${NAV}`,
    '.chip { position: relative } .chip::before { content: ""; position: absolute; inset: -4px 0 }',
  )

  test('a clean phone page reports nothing', async ({ page }) => {
    expect(await violationsOf(page, CLEAN)).toEqual([])
  })

  test('the helper reports a planted overflow', async ({ page }) => {
    const v = await violationsOf(page, html('<main><div style="width:500px;height:40px;background:#ddd">wide</div></main>'))
    // Both the document-level check and the per-element one fire.
    expect(v.some((line) => line.startsWith('overflow: the document scrolls sideways'))).toBe(true)
    expect(v.some((line) => line.startsWith('overflow: <div') && line.includes('spans'))).toBe(true)
  })

  test('the helper reports a segmented control clipped at the screen edge', async ({ page }) => {
    const button = '<button style="min-height:44px;white-space:nowrap;width:130px">Option</button>'
    const seg = `<div role="radiogroup" style="display:flex;width:520px">${button.repeat(4)}</div>`
    // Spilling past the edge, and cut off by an overflow:hidden parent.
    const spilled = await violationsOf(page, html(`<main>${seg}</main>`))
    expect(spilled.some((line) => line.startsWith('overflow: <div') && line.includes('Option') && line.includes('spans'))).toBe(true)
    const clipped = await violationsOf(page, html(`<main><div style="overflow:hidden;width:390px">${seg}</div></main>`))
    expect(kinds(clipped)).toContain('clip')
  })

  test('data-hm-clip-ok and allowClip exempt an intentional clip', async ({ page }) => {
    const strip = (attr: string) => `<main><div ${attr} style="overflow:hidden;width:300px"><div style="width:600px;height:44px">long</div></div></main>`
    expect(kinds(await violationsOf(page, html(strip(''))))).toContain('clip')
    expect(await violationsOf(page, html(strip('data-hm-clip-ok')))).toEqual([])
    expect(await violationsOf(page, html(strip('class="allowed"')), ['.allowed'])).toEqual([])
  })

  test('the helper reports a 30 px button and a 36 px chip without its hit area', async ({ page }) => {
    expect(kinds(await violationsOf(page, html('<main><button style="height:30px;min-width:44px">Go</button></main>')))).toContain('target')
    // data-hm-hit="36" without the ::before extension is still a violation; the clean page proves the extended one passes.
    const bare = await violationsOf(page, html('<main><button data-hm-hit="36" style="height:36px;min-width:44px">Chip</button></main>'))
    expect(bare.some((line) => line.startsWith('target:') && line.includes('::before'))).toBe(true)
  })

  test('the helper reports 10 px text and a 13 px input', async ({ page }) => {
    expect(kinds(await violationsOf(page, html('<main><p style="font-size:10px">tiny</p></main>')))).toContain('text-font')
    expect(kinds(await violationsOf(page, html('<main><input aria-label="Name" style="font-size:13px;height:44px"></main>')))).toContain('input-font')
  })

  test('the helper reports a label wrapped inside a one-line box', async ({ page }) => {
    // The 44 px minimum height would hide a two-line label from a plain height check, so lines are counted.
    const seg = '<div role="radiogroup"><button style="min-height:44px;width:90px">Services needing attention</button></div>'
    expect(kinds(await violationsOf(page, html(`<main>${seg}</main>`)))).toContain('wrap')
    const tab = '<div role="tab" style="min-height:44px;width:70px">Task definition</div>'
    expect(kinds(await violationsOf(page, html(`<main>${tab}</main>`)))).toContain('wrap')
    const oneline = '<div data-hm-oneline style="width:60px">Not allowed to wrap</div>'
    expect(kinds(await violationsOf(page, html(`<main>${oneline}</main>`)))).toContain('wrap')
  })

  test('an open sheet covering the page is not reported, but a covered row inside the sheet is', async ({ page }) => {
    const row = (name: string, cover: string) =>
      `<div data-hm-row style="position:relative;height:60px"><span style="position:absolute;left:8px;top:20px">${name}</span>${cover}</div>`
    const scrim = '<div style="position:fixed;inset:0;background:#000"></div>'
    const sheet = (inner: string) => `<div role="dialog" aria-modal="true" style="position:fixed;left:0;right:0;bottom:0;height:200px;background:#fff">${inner}</div>`
    const covering = '<div style="position:absolute;left:0;top:0;width:200px;height:60px;background:#fff">covering column</div>'
    expect(kinds(await violationsOf(page, html(`<main>${row('payments-api', '')}</main>${scrim}${sheet('<p>Actions</p>')}`)))).not.toContain('covered')
    expect(kinds(await violationsOf(page, html(`<main><p>Page</p></main>${scrim}${sheet(row('payments-api', covering))}`)))).toContain('covered')
  })

  test('the helper reports a sticky column covering a row name', async ({ page }) => {
    const covering = `<div data-hm-row style="position:relative;height:60px"><span style="position:absolute;left:8px;top:20px">payments-api-long-name</span>
      <div style="position:absolute;left:0;top:0;width:200px;height:60px;background:#fff">covering column</div></div>`
    expect(kinds(await violationsOf(page, html(`<main>${covering}</main>`)))).toContain('covered')
    const offscreen = `<div data-hm-row style="position:relative;height:60px"><span style="position:absolute;left:380px;top:20px;white-space:nowrap">payments-api-long-name</span></div>`
    expect(kinds(await violationsOf(page, html(`<main>${offscreen}</main>`)))).toContain('row-bounds')
  })

  test('the helper reports content hidden under the tab bar when scrolled to the end', async ({ page }) => {
    const content = (padding: number) => `<main style="padding-bottom:${padding}px"><div style="height:1200px">tall</div><p style="margin:0;height:40px">last line</p></main>${NAV}`
    expect(kinds(await violationsOf(page, html(content(0))))).toContain('bottom')
    // With the inset padding the last element clears the bar.
    expect(kinds(await violationsOf(page, html(content(72))))).not.toContain('bottom')
  })

  test('the helper reports an empty labelled section', async ({ page }) => {
    expect(kinds(await violationsOf(page, html('<main><section aria-label="Pinned"></section></main>')))).toContain('section-empty')
  })

  test('watchPage turns a page error into a failure of expectPhoneLayoutClean', async ({ page }) => {
    const problems = watchPage(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.setContent(html('<main><p>ok</p></main>'))
    await page.evaluate(() => {
      setTimeout(() => {
        throw new Error('planted page error')
      }, 0)
    })
    await expect.poll(() => problems.join('\n')).toContain('planted page error')
    await expect(expectPhoneLayoutClean(page)).rejects.toThrow(/planted page error/)
  })
})
