import { Fragment, type ReactNode } from 'react'
import { Link, useInRouterContext } from 'react-router'
import styles from './agent.module.css'

/**
 * Renders a model's answer from plain text into React elements only. The text can come from untrusted logs, so there is no
 * HTML, no image element and no external link: those render as the literal text they were written as.
 */

/** A path on this site: one leading "/" that is not "//" or "/\", with no whitespace or control characters (browsers drop tabs and newlines inside a URL). */
const SAME_ORIGIN_PATH = /^\/(?![/\\])[^\s\\]*$/

const hasControlCharacter = (value: string) => [...value].some((char) => char.charCodeAt(0) < 0x20 || char.charCodeAt(0) === 0x7f)

export const isSameOriginPath = (target: string): boolean => SAME_ORIGIN_PATH.test(target) && !hasControlCharacter(target)

// code span | bold | image or link (the target may hold one level of parentheses)
const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(!?)\[([^\]\n]*)\]\(((?:[^()\s]|\([^()\s]*\))*)\)/g

function AnswerLink({ to, children }: { to: string; children: ReactNode }) {
  const inRouter = useInRouterContext()
  return inRouter ? <Link to={to}>{children}</Link> : <a href={to}>{children}</a>
}

function renderInline(text: string): ReactNode[] {
  const out: ReactNode[] = []
  let last = 0
  let key = 0
  for (const match of text.matchAll(INLINE)) {
    const index = match.index ?? 0
    if (index > last) out.push(text.slice(last, index))
    const [whole, code, bold, bang, label, target] = match
    if (code) {
      out.push(<code key={key++}>{code.slice(1, -1)}</code>)
    } else if (bold) {
      out.push(<strong key={key++}>{bold.slice(2, -2)}</strong>)
    } else if (bang) {
      out.push(whole) // an image is never fetched: show what was written
    } else if (isSameOriginPath(target)) {
      out.push(
        <AnswerLink key={key++} to={target}>
          {label || target}
        </AnswerLink>,
      )
    } else {
      out.push(label && label !== target ? `${label} (${target})` : target)
    }
    last = index + whole.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out.map((node, i) => (typeof node === 'string' ? <Fragment key={`t${i}`}>{node}</Fragment> : node))
}

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
      const children = items.map((item, n) => <li key={n}>{renderInline(item)}</li>)
      blocks.push(ordered ? <ol key={blocks.length}>{children}</ol> : <ul key={blocks.length}>{children}</ul>)
    } else if (line.trim() === '') {
      i++
    } else {
      const paragraph: string[] = []
      while (i < lines.length && lines[i].trim() !== '' && !FENCE.test(lines[i]) && !BULLET.test(lines[i]) && !NUMBERED.test(lines[i])) paragraph.push(lines[i++])
      blocks.push(<p key={blocks.length}>{renderInline(paragraph.join('\n'))}</p>)
    }
  }
  return <div className={styles.answer}>{blocks}</div>
}
