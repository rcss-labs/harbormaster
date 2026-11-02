import { describe, expect, it } from 'vitest'
import { changedLineNumbers, diffLines } from './diffLines'

const lines = (n: number) => Array.from({ length: n }, (_, i) => `line${i + 1}`)

describe('diffLines', () => {
  it('marks identical inputs as unchanged', () => {
    const out = diffLines('a\nb\nc', 'a\nb\nc')
    expect(out.every((l) => l.kind === ' ' || l.kind === 'gap')).toBe(true)
    expect(out.some((l) => l.kind === '+' || l.kind === '-')).toBe(false)
  })

  it('shows a changed line with two context lines each side and a gap', () => {
    const before = lines(10)
    const after = [...before]
    after[4] = 'changed'
    const out = diffLines(before.join('\n'), after.join('\n'))
    expect(out).toEqual([
      { kind: 'gap', text: '⋯' },
      { kind: ' ', text: 'line3' },
      { kind: ' ', text: 'line4' },
      { kind: '-', text: 'line5' },
      { kind: '+', text: 'changed' },
      { kind: ' ', text: 'line6' },
      { kind: ' ', text: 'line7' },
      { kind: 'gap', text: '⋯' },
    ])
  })

  it('reports an added trailing line as one +', () => {
    const out = diffLines('a\nb', 'a\nb\nc')
    expect(out.filter((l) => l.kind === '+')).toEqual([{ kind: '+', text: 'c' }])
    expect(out.filter((l) => l.kind === '-')).toHaveLength(0)
  })
})

describe('changedLineNumbers', () => {
  it('is empty when nothing changed', () => {
    expect(changedLineNumbers('a\nb', 'a\nb').size).toBe(0)
  })

  it('numbers (1-based) the lines of the new text that were added or edited', () => {
    expect([...changedLineNumbers('a\nb\nc', 'a\nB\nc\nd')]).toEqual([2, 4])
  })

  it('ignores deleted lines, which have no line in the new text', () => {
    expect(changedLineNumbers('a\nb\nc', 'a\nc').size).toBe(0)
  })
})
