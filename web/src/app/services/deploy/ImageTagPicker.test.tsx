import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { imageTagsHandler, imageTagsNoneHandler, imageTagsResponse } from '../../../test/imageTags'
import { server } from '../../../test/msw'
import { renderWithProviders } from '../../../test/setup'
import { ImageTagPicker } from './ImageTagPicker'

const renderPicker = (overrides: Partial<React.ComponentProps<typeof ImageTagPicker>> = {}) => {
  const onChange = vi.fn()
  renderWithProviders(<ImageTagPicker serviceId="svc" container="payments-api" currentTag="1.18.2" deployedTag="1.18.2" onChange={onChange} {...overrides} />)
  return onChange
}

describe('ImageTagPicker', () => {
  it('shows 10 newest with pushed time and marks deployed', async () => {
    server.use(imageTagsHandler())
    const user = userEvent.setup()
    renderPicker()

    const input = screen.getByRole('combobox', { name: 'Image tag' })
    expect(input).toHaveValue('1.18.2')
    await user.click(input)

    const options = await screen.findAllByRole('option')
    expect(options).toHaveLength(10)
    expect(options[0]).toHaveTextContent('1.18.4')
    expect(options[0]).toHaveTextContent('newest')
    expect(options[0]).toHaveTextContent(/ago/)
    const deployed = screen.getByRole('option', { name: /1\.18\.2/ })
    expect(deployed).toHaveTextContent('deployed')
    expect(deployed).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText('10 of 24 tags')).toBeInTheDocument()
    expect(input).toHaveAttribute('aria-expanded', 'true')
  })

  it('typing searches all tags via ?q and lists matches', async () => {
    const requests: string[] = []
    server.use(
      http.get('/api/services/:id/image-tags', ({ request }) => {
        requests.push(request.url)
        return HttpResponse.json(imageTagsResponse(request.url))
      }),
    )
    const user = userEvent.setup()
    const onChange = renderPicker()

    const input = screen.getByRole('combobox', { name: 'Image tag' })
    await user.click(input)
    await screen.findAllByRole('option')
    await user.clear(input)
    await user.type(input, '1.16')

    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(9))
    for (const option of screen.getAllByRole('option')) expect(option).toHaveTextContent('1.16.')
    expect(screen.getByText('9 of 24 tags match')).toBeInTheDocument()
    expect(requests.some((url) => new URL(url).searchParams.get('q') === '1.16')).toBe(true)
    // Debounced: the keystrokes in between were not each sent as a search.
    expect(requests.filter((url) => new URL(url).searchParams.has('q')).length).toBeLessThan(4)

    await user.click(within(screen.getByRole('listbox')).getByRole('option', { name: /1\.16\.4/ }))
    expect(onChange).toHaveBeenCalledWith('1.16.4')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  })

  it('free text when source is none and validates the tag', async () => {
    server.use(imageTagsNoneHandler())
    const user = userEvent.setup()
    const onChange = renderPicker({ currentTag: '1.0.0', deployedTag: '1.0.0' })

    const input = await screen.findByRole('combobox', { name: 'Image tag' })
    await waitFor(() => expect(screen.getByText(/not in ECR/i)).toBeInTheDocument())
    await user.clear(input)
    await user.type(input, 'v1 2')
    expect(screen.getByRole('alert')).toHaveTextContent(/letters, digits/)
    expect(onChange).not.toHaveBeenCalledWith('v1 2')

    await user.clear(input)
    await user.type(input, '1.2.3')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(onChange).toHaveBeenLastCalledWith('1.2.3')
  })
})
