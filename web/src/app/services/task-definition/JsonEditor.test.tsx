import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { JsonEditor } from './JsonEditor'

const noop = vi.fn()

describe('JsonEditor', () => {
  it('marks edited lines in the gutter and labels the review button with the revision to register', () => {
    render(
      <JsonEditor
        text={'{\n  "cpu": "1024",\n  "memory": "512"\n}'}
        initialText={'{\n  "cpu": "512",\n  "memory": "512"\n}'}
        registerRevision={44}
        error={null}
        dirty
        readOnly={false}
        onChange={noop}
        onDiscard={noop}
        onReview={noop}
      />,
    )
    const gutter = screen.getByTestId('json-gutter')
    expect([...gutter.children].map((row) => row.hasAttribute('data-changed'))).toEqual([false, true, false, false])
    expect(screen.getByRole('button', { name: 'Review and register :44' })).toBeEnabled()
  })

  it('shows no marks when the text matches the deployed definition', () => {
    render(
      <JsonEditor text="a" initialText="a" registerRevision={3} error={null} dirty={false} readOnly={false} onChange={noop} onDiscard={noop} onReview={noop} />,
    )
    expect(screen.getByTestId('json-gutter').querySelector('[data-changed]')).toBeNull()
    expect(screen.getByRole('button', { name: 'Review and register :3' })).toBeDisabled()
  })
})
