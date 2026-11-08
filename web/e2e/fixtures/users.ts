import { SEED_PASSWORD } from '../helpers'

// Seeded by internal/devseed/devseed.go (served by `serve --fake-aws`); all share the e2e password.
export const USERS = {
  sam: { username: 'sam', password: SEED_PASSWORD, admin: true },
  dana: { username: 'dana', password: SEED_PASSWORD, admin: false },
  omer: { username: 'omer', password: SEED_PASSWORD, admin: false },
  oncall: { username: 'oncall', password: SEED_PASSWORD, admin: false },
} as const

export type Role = keyof typeof USERS
