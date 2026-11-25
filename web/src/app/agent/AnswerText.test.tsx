import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AnswerText } from './AnswerText'

const show = (text: string) => render(<MemoryRouter><AnswerText text={text} /></MemoryRouter>)

describe('AnswerText', () => {
  it('renders lists and code', () => {
    const { container } = show('Intro with **bold** and `inline`.\n\n- one\n- two\n\n1. first\n2. second\n\n```\nline a\n  line b\n```')
    expect(container.querySelectorAll('ul > li')).toHaveLength(2)
    expect(container.querySelectorAll('ol > li')).toHaveLength(2)
    expect(container.querySelector('strong')).toHaveTextContent('bold')
    expect(container.querySelector('p code')).toHaveTextContent('inline')
    expect(container.querySelector('pre code')?.textContent).toBe('line a\n  line b')
  })

  it('never renders an img element', () => {
    const { container } = show('![pixel](https://evil.example/p.png) and ![x](/local.png)')
    expect(container.querySelector('img')).toBeNull()
    expect(container).toHaveTextContent('![pixel](https://evil.example/p.png)')
  })

  it('external link is plain text', () => {
    const { container } = show('See [docs](https://example.com/a) and [proto](//example.com/x) and [back](/\\example.com)')
    expect(container.querySelector('a')).toBeNull()
    expect(container).toHaveTextContent('https://example.com/a')
    expect(container).toHaveTextContent('//example.com/x')
    expect(container).toHaveTextContent('/\\example.com')
  })

  it('same-origin path is a link', () => {
    show('Open [the service](/services/abc?x=1)')
    expect(screen.getByRole('link', { name: 'the service' })).toHaveAttribute('href', '/services/abc?x=1')
  })

  it('a path that is not an estate page is plain text', () => {
    const { container } = show('[a](/settings/users) [b](/me/access) [c](/oauth/authorize) [d](/tasks/abc/terminal) [e](/services/../settings) [f](/%73ettings)')
    expect(container.querySelector('a')).toBeNull()
    expect(container).toHaveTextContent('a (/settings/users)')
    expect(container).toHaveTextContent('e (/services/../settings)')
  })

  it('javascript: link is plain text', () => {
    const { container } = show('[click](javascript:alert(1)) [tab](/\t/example.com) [data](data:text/html,x)')
    expect(container.querySelector('a')).toBeNull()
    expect(container).toHaveTextContent('javascript:alert(1)')
  })

  it('raw HTML is shown as text', () => {
    const { container } = show('<script>alert(1)</script> <b>no</b> <img src=x onerror=alert(1)>')
    expect(container.querySelector('script, b, img')).toBeNull()
    expect(container).toHaveTextContent('<script>alert(1)</script>')
    expect(container).toHaveTextContent('<img src=x onerror=alert(1)>')
  })

  it('api paths are plain text', () => {
    const { container } = show('[a](/api/auth/logout) [b](/API/x) [c](/api?x=1) [ok](/services/x)')
    expect(container.querySelectorAll('a')).toHaveLength(1)
    expect(screen.getByRole('link', { name: 'ok' })).toBeInTheDocument()
    expect(container).toHaveTextContent('/api/auth/logout')
  })

  it.each(['[', '*', '**', '`', '![', '(', '[](', ']'])('stays fast on 50,000 of %s', (opener) => {
    const started = performance.now()
    const { container } = show(opener.repeat(50_000))
    expect(performance.now() - started).toBeLessThan(1000)
    expect(container.querySelector('a, img')).toBeNull()
  })
})
