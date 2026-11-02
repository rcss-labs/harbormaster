export type DiffLine = { kind: ' ' | '+' | '-' | 'gap'; text: string }

// Line diff via longest common subsequence. Unchanged runs further than
// `context` lines from any change collapse into a single 'gap' row.
function fullDiff(before: string, after: string): DiffLine[] {
  const a = before.split('\n')
  const b = after.split('\n')
  const n = a.length
  const m = b.length
  const lcs = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }

  const full: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) {
      full.push({ kind: ' ', text: a[i] })
      i++
      j++
    } else if (i < n && (j >= m || lcs[i + 1][j] >= lcs[i][j + 1])) {
      full.push({ kind: '-', text: a[i] })
      i++
    } else {
      full.push({ kind: '+', text: b[j] })
      j++
    }
  }

  return full
}

/** 1-based numbers of the lines in `after` that were added or edited; deleted lines have no line to mark. */
export function changedLineNumbers(before: string, after: string): Set<number> {
  const changed = new Set<number>()
  let line = 0
  for (const row of fullDiff(before, after)) {
    if (row.kind === '-') continue
    line++
    if (row.kind === '+') changed.add(line)
  }
  return changed
}

export function diffLines(before: string, after: string, context = 2): DiffLine[] {
  const full = fullDiff(before, after)
  const out: DiffLine[] = []
  let inGap = false
  full.forEach((line, index) => {
    const near = full.slice(Math.max(0, index - context), index + context + 1).some((l) => l.kind !== ' ')
    if (near) {
      out.push(line)
      inGap = false
    } else if (!inGap) {
      out.push({ kind: 'gap', text: '⋯' })
      inGap = true
    }
  })
  return out
}
