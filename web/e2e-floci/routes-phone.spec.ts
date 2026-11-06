import { PHONE_FIXME_REASON, PHONE_ROUTES, phoneFixme, runPhonePass } from '../e2e/fixtures/phone'
import { test } from '../e2e/fixtures/test'
import { isExpectedFailure } from './allowlist'
import { resolveTargets } from './targets'

// The phone pass over the floci estate, as admin, at 390 x 844 (project routes-phone). Every route in each theme goes
// through the generic phone assertions (e2e/fixtures/phone.ts), then the first row action sheet, when the page has
// one, is opened and checked too. Routes in PHONE_FIXME are skipped until the phone layer is merged.

const THEMES = ['dark', 'light'] as const

for (const route of PHONE_ROUTES().filter((r) => r.name !== 'login')) {
  test.describe(route.name, () => {
    for (const theme of THEMES) {
      test(`390px ${theme}`, async ({ page, request }) => {
        test.fixme(phoneFixme(route.name), PHONE_FIXME_REASON)
        const path = route.path(await resolveTargets(request))
        test.skip(path === null, `${route.name} is not available on this branch or estate`)
        await runPhonePass(page, { routeName: route.name, path: path!, theme, isExpectedFailure })
      })
    }
  })
}
