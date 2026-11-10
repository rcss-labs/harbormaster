import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { MultiSelect } from './MultiSelect'

const options = [
  { value: '2', label: 'Staging' },
  { value: '3', label: 'Prod' },
]

const setup = (value: string[] = [], onChange = vi.fn()) => {
  render(<MultiSelect label="Tag filter" placeholder="All tags" options={options} value={value} onChange={onChange} />)
  return onChange
}

describe('MultiSelect', () => {
  it('shows the placeholder when nothing is selected', () => {
    setup()
    expect(screen.getByRole('button', { name: 'Tag filter' })).toHaveTextContent('All tags')
  })

  it('lists options as checkboxes and reports toggles', async () => {
    const onChange = setup()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Tag filter' }))
    expect(screen.getByRole('group', { name: 'Tag filter options' })).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: 'Prod' }))
    expect(onChange).toHaveBeenCalledWith(['3'])
  })

  it('button text lists the selected labels', () => {
    setup(['2', '3'])
    expect(screen.getByRole('button', { name: 'Tag filter' })).toHaveTextContent('Staging, Prod')
  })

  it('Escape closes the popover', async () => {
    setup()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Tag filter' }))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('group', { name: 'Tag filter options' })).not.toBeInTheDocument()
  })

  describe('searchable', () => {
    const tagOptions = [
      { value: 'hm:2', label: 'Staging', group: 'Harbormaster tags', hint: '4 services' },
      { value: 'aws:env=prod', label: 'env=prod', group: 'AWS tags', keywords: 'production' },
    ]
    const renderSearchable = (props = {}) => {
      const onChange = vi.fn()
      render(<MultiSelect label="Tags" placeholder="Search" options={tagOptions} value={[]} onChange={onChange} searchable {...props} />)
      return onChange
    }

    it('filters by label and keywords, under group headings', async () => {
      renderSearchable()
      const user = userEvent.setup()
      await user.click(screen.getByRole('button', { name: 'Tags' }))
      expect(screen.getByText('Harbormaster tags')).toBeInTheDocument()
      expect(screen.getByText('AWS tags')).toBeInTheDocument()
      await user.type(screen.getByRole('searchbox', { name: 'Search Tags' }), 'production')
      expect(screen.getByRole('checkbox', { name: 'env=prod' })).toBeInTheDocument()
      expect(screen.queryByRole('checkbox', { name: /Staging/ })).not.toBeInTheDocument()
      expect(screen.queryByText('Harbormaster tags')).not.toBeInTheDocument()
    })

    it('says so when nothing matches', async () => {
      renderSearchable()
      const user = userEvent.setup()
      await user.click(screen.getByRole('button', { name: 'Tags' }))
      await user.type(screen.getByRole('searchbox', { name: 'Search Tags' }), 'zzz')
      expect(screen.getByText('Nothing matches.')).toBeInTheDocument()
    })

    it('offers to add what was typed and reports its value', async () => {
      const onChange = renderSearchable({ creatable: (q: string) => ({ value: `aws:${q}`, label: q }) })
      const user = userEvent.setup()
      await user.click(screen.getByRole('button', { name: 'Tags' }))
      await user.type(screen.getByRole('searchbox', { name: 'Search Tags' }), 'a=b')
      await user.click(screen.getByRole('button', { name: 'Add a=b' }))
      expect(onChange).toHaveBeenCalledWith(['aws:a=b'])
    })

    it('shows the selection as removable chips', async () => {
      const onChange = renderSearchable({ chips: true, value: ['hm:2', 'aws:env=prod'] })
      const user = userEvent.setup()
      expect(screen.getByRole('button', { name: 'Tags' })).toHaveTextContent('Search')
      await user.click(screen.getByRole('button', { name: 'Remove Staging' }))
      expect(onChange).toHaveBeenCalledWith(['aws:env=prod'])
    })
  })
})
