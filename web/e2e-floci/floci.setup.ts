import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test as setup } from '@playwright/test'

const here = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(here, '..', '..')
const passwordFile = path.join(repoRoot, 'tools', 'floci', '.admin-password')
const authFile = path.join(here, '..', '.auth-floci', 'admin.json')

setup.setTimeout(240_000)

// connect.sh signs in (finishing the forced password change and rewriting the password file), connects the floci
// account, tags, creates omer and makes the F7 changes. It is idempotent.
setup('connect Harbormaster to the floci estate', async () => {
  execFileSync(path.join(repoRoot, 'tools', 'floci', 'connect.sh'), { stdio: 'inherit', env: process.env })
})

setup('sign in as admin and keep the session', async ({ request }) => {
  const password = fs.readFileSync(passwordFile, 'utf8').trim()
  const response = await request.post('/api/auth/login', { data: { username: 'admin', password } })
  expect(response.status(), 'admin sign-in').toBe(200)
  const me = await (await request.get('/api/me')).json()
  expect(me.must_change_password).toBe(false)
  fs.mkdirSync(path.dirname(authFile), { recursive: true })
  await request.storageState({ path: authFile })
})
