import { http, HttpResponse } from 'msw'
import type { ImageTagsResponse } from '../api/types'
import { FIXTURE_NOW } from './fixtures'

type TagEntry = ImageTagsResponse['tags'][number]

/** Newest first, one tag pushed an hour before the previous one. */
function tagList(names: string[]): TagEntry[] {
  const now = Date.parse(FIXTURE_NOW)
  return names.map((tag, position) => ({ tag, pushed_at: new Date(now - (position + 1) * 3_600_000).toISOString() }))
}

const descending = (from: number, to: number, prefix: string) => {
  const names: string[] = []
  for (let patch = from; patch >= to; patch--) names.push(`${prefix}${patch}`)
  return names
}

/** Mirrors the fake: 24 ECR tags for payments-api, 1.18.4 down to 1.16.1. */
export const PAYMENTS_TAGS = tagList([...descending(4, 0, '1.18.'), ...descending(9, 0, '1.17.'), ...descending(9, 1, '1.16.')])
/** Mirrors the fake: the staging `api` sidecar `migrator` has 6 tags. */
export const MIGRATOR_TAGS = tagList(['2.3.0', '2.2.1', '2.2.0', '2.1.0', '2.0.1', '2.0.0'])

export interface RepoFixture {
  image: string
  repository: string
  current: string
  tags: TagEntry[]
}

export const REPOS: Record<string, RepoFixture> = {
  'payments-api': {
    image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api',
    repository: 'payments-api',
    current: '1.18.2',
    tags: PAYMENTS_TAGS,
  },
  migrator: {
    image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/migrator',
    repository: 'migrator',
    current: '2.2.1',
    tags: MIGRATOR_TAGS,
  },
}

/** Answers `image-tags` like the real endpoint: `container`, case-insensitive `q`, `limit` (default 10, max 200). */
export function imageTagsResponse(url: string, repos: Record<string, RepoFixture> = REPOS): ImageTagsResponse {
  const params = new URL(url).searchParams
  const container = params.get('container') || 'payments-api'
  const repo = repos[container]
  const q = (params.get('q') ?? '').toLowerCase()
  const limit = Math.min(Number(params.get('limit') ?? 10) || 10, 200)
  if (!repo) {
    return { source: 'none', container, image: '', repository: '', current: '', total: 0, matched: 0, truncated: false, tags: [] }
  }
  const matching = repo.tags.filter((entry) => entry.tag.toLowerCase().includes(q))
  return {
    source: 'ecr',
    container,
    image: repo.image,
    repository: repo.repository,
    current: repo.current,
    total: repo.tags.length,
    matched: matching.length,
    truncated: false,
    tags: matching.slice(0, limit),
  }
}

/** A msw handler for the service image-tags route backed by the fake repositories. */
export const imageTagsHandler = (repos: Record<string, RepoFixture> = REPOS) =>
  http.get('/api/services/:id/image-tags', ({ request }) => HttpResponse.json(imageTagsResponse(request.url, repos)))

/** A handler for a container whose registry is not ECR. */
export const imageTagsNoneHandler = () =>
  http.get('/api/services/:id/image-tags', ({ request }) =>
    HttpResponse.json({
      source: 'none',
      container: new URL(request.url).searchParams.get('container') ?? '',
      image: '',
      repository: '',
      current: '',
      total: 0,
      matched: 0,
      truncated: false,
      tags: [],
    } satisfies ImageTagsResponse),
  )
