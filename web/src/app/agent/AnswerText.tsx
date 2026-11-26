import { Fragment, memo, type ReactNode } from 'react'
import { Link, useInRouterContext } from 'react-router'
import { isEstatePath } from './estateLinks'
import styles from './agent.module.css'

/**
 * Renders a model's answer from plain text into React elements only. The text can come from untrusted logs, so there is no
 * HTML, no image element and no external link: those render as the literal text they were written as.
 */

function AnswerLink({ to, children }: { to: string; children: ReactNode }) {
  const inRouter = useInRouterContext()
  return inRouter ? <Link to={to}>{children}</Link> : <a href={to}>{children}</a>
}

/** The next index of `char` at or after `from`, remembering the last answer: queries only move forward, so the whole scan stays linear. */
function nextFinder(text: string, char: string) {
  let from = 0
  let found = -2 // -2: nothing cached yet
  return (at: number): number => {
    if (found === -1 && at >= from) return -1
    if (found >= at && at >= from) return found
    from = at
    found = text.indexOf(char, at)
    return found
  }
}

const MAX_TARGET = 2000

/** End (exclusive) of a link target starting at `start` (after the opening parenthesis), allowing one level of parentheses; -1 if it is not one. */
function targetEnd(text: string, start: number): number {
  let depth = 0
  for (let i = start; i < text.length && i - start <= MAX_TARGET; i++) {
    const char = text[i]
    if (char === '(') {
      if (++depth > 1) return -1
    } else if (char === ')') {
      if (depth === 0) return i
      depth--
    } else if (/\s/.test(char)) {
      return -1
    }
  }
  return -1
}

/** Inline code, bold, and links or images, found in one left-to-right pass with no backtracking, so hostile text cannot make it slow. */
function renderInline(text: string): ReactNode[] {
  const out: ReactNode[] = []
  const nextNewline = nextFinder(text, '\n')
  const nextBacktick = nextFinder(text, '`')
  const nextStar = nextFinder(text, '*')
  const nextClose = nextFinder(text, ']')
  const sameLine = (from: number, to: number) => {
    const newline = nextNewline(from)
    return newline === -1 || newline > to
  }
  let last = 0
  let key = 0
  const flush = (upTo: number) => {
    if (upTo > last) out.push(text.slice(last, upTo))
  }
  let i = 0
  while (i < text.length) {
    const char = text[i]
    if (char === '`') {
      const close = nextBacktick(i + 1)
      if (close > i + 1 && sameLine(i + 1, close)) {
        flush(i)
        out.push(<code key={key++}>{text.slice(i + 1, close)}</code>)
        i = last = close + 1
        continue
      }
    } else if (char === '*' && text[i + 1] === '*') {
      const close = nextStar(i + 2)
      if (close > i + 2 && text[close + 1] === '*' && sameLine(i + 2, close)) {
        flush(i)
        out.push(<strong key={key++}>{text.slice(i + 2, close)}</strong>)
        i = last = close + 2
        continue
      }
    } else if (char === '[' || (char === '!' && text[i + 1] === '[')) {
      const image = char === '!'
      const open = image ? i + 1 : i
      const close = nextClose(open + 1)
      if (close !== -1 && text[close + 1] === '(' && sameLine(open + 1, close)) {
        const end = targetEnd(text, close + 2)
        if (end !== -1) {
          const label = text.slice(open + 1, close)
          const target = text.slice(close + 2, end)
          flush(i)
          if (image) {
            out.push(text.slice(i, end + 1)) // an image is never fetched: show what was written
          } else if (isEstatePath(target)) {
            out.push(
              <AnswerLink key={key++} to={target}>
                {label || target}
              </AnswerLink>,
            )
          } else {
            out.push(label && label !== target ? `${label} (${target})` : target)
          }
          i = last = end + 1
          continue
        }
      }
    }
    i++
  }
  flush(text.length)
  return out.map((node, n) => (typeof node === 'string' ? <Fragment key={`t${n}`}>{node}</Fragment> : node))
}

/** A finished block keeps the same text while later ones stream in, so memo skips re-parsing it. */
const Inline = memo(function Inline({ text }: { text: string }) {
  return <>{renderInline(text)}</>
})

const BULLET = /^\s{0,3}[-*]\s+(.*)$/
const NUMBERED = /^\s{0,3}\d{1,3}[.)]\s+(.*)$/
const FENCE = /^\s{0,3}```/

export function AnswerText({ text }: { text: string }) {
  const lines = text.replace(/\r\n?/g, '\n').split('\n')
  const blocks: ReactNode[] = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    if (FENCE.test(line)) {
      const code: string[] = []
      i++
      while (i < lines.length && !FENCE.test(lines[i])) code.push(lines[i++])
      i++ // the closing fence; an unclosed block runs to the end of the text
      blocks.push(
        <pre key={blocks.length} className={styles.codeBlock}>
          <code>{code.join('\n')}</code>
        </pre>,
      )
    } else if (BULLET.test(line) || NUMBERED.test(line)) {
      const ordered = NUMBERED.test(line)
      const pattern = ordered ? NUMBERED : BULLET
      const items: string[] = []
      while (i < lines.length && pattern.test(lines[i])) items.push(lines[i++].replace(pattern, '$1'))
      const children = items.map((item, n) => <li key={n}><Inline text={item} /></li>)
      blocks.push(ordered ? <ol key={blocks.length}>{children}</ol> : <ul key={blocks.length}>{children}</ul>)
    } else if (line.trim() === '') {
      i++
    } else {
      const paragraph: string[] = []
      while (i < lines.length && lines[i].trim() !== '' && !FENCE.test(lines[i]) && !BULLET.test(lines[i]) && !NUMBERED.test(lines[i])) paragraph.push(lines[i++])
      blocks.push(<p key={blocks.length}>
          <Inline text={paragraph.join('\n')} />
        </p>)
    }
  }
  return <div className={styles.answer}>{blocks}</div>
}
