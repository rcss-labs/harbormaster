import { describe, expect, it } from 'vitest'
import { revisionDiff, triggerSentence } from './revisionDiff'

function definition(fields: { cpu?: string; memory?: string; containers: Record<string, unknown>[] }) {
  return { family: 'resizer', cpu: fields.cpu ?? '512', memory: fields.memory ?? '1024', containerDefinitions: fields.containers }
}

describe('revisionDiff', () => {
  it('reports image and memory like the mockup', () => {
    const before = definition({ memory: '1024', containers: [{ name: 'app', image: 'resizer:0.4.1' }] })
    const after = definition({ memory: '512', containers: [{ name: 'app', image: 'resizer:0.4.2' }] })
    expect(revisionDiff(before, after)).toEqual([
      { label: 'memory', from: '1024', to: '512' },
      { label: 'image', from: 'resizer:0.4.1', to: 'resizer:0.4.2' },
    ])
  })

  it('handles several containers and added env', () => {
    const before = definition({
      containers: [
        { name: 'app', image: 'app:1', environment: [{ name: 'LOG', value: 'info' }] },
        { name: 'proxy', image: 'proxy:1', secrets: [{ name: 'TOKEN', valueFrom: 'arn:old' }] },
      ],
    })
    const after = definition({
      cpu: '1024',
      containers: [
        { name: 'app', image: 'app:2', environment: [{ name: 'LOG', value: 'info' }, { name: 'BATCH', value: '64' }] },
        { name: 'proxy', image: 'proxy:1' },
      ],
    })
    expect(revisionDiff(before, after)).toEqual([
      { label: 'cpu', from: '512', to: '1024' },
      { label: 'image app', from: 'app:1', to: 'app:2' },
      { label: 'env BATCH', from: 'none', to: '64' },
      { label: 'secret TOKEN', from: 'arn:old', to: 'none' },
    ])
  })

  it('returns no rows for malformed input', () => {
    expect(revisionDiff(null, 'x')).toEqual([])
    expect(revisionDiff({}, { containerDefinitions: 5 })).toEqual([])
  })
})

describe('triggerSentence', () => {
  it('prefers memory and says lowered or raised', () => {
    const memory = { label: 'memory', from: '1024', to: '512' }
    const cpu = { label: 'cpu', from: '256', to: '512' }
    const image = { label: 'image', from: '0.4.1', to: '0.4.2' }
    expect(triggerSentence([image, cpu, memory])).toBe('It lowered memory from 1024 to 512 MiB.')
    expect(triggerSentence([image, cpu])).toBe('It raised CPU from 256 to 512.')
    expect(triggerSentence([image])).toBe('It changed the image from 0.4.1 to 0.4.2.')
    expect(triggerSentence([{ label: 'memory', from: '512', to: '1024' }])).toBe('It raised memory from 512 to 1024 MiB.')
    expect(triggerSentence([{ label: 'env LOG', from: 'none', to: 'debug' }])).toBeNull()
  })
})
