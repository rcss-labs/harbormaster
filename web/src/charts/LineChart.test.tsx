import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../test/phone'
import { renderWithProviders } from '../test/setup'
import { LineChart } from './LineChart'

const count = (text: string, char: string) => text.split(char).length - 1

describe('LineChart with gaps', () => {
  it('breaks the path at null values', () => {
    const { container } = renderWithProviders(
      <LineChart series={[{ name: 'cpu', color: 'red', values: [1, null, 2, 3] }]} xLabels={['a', 'b']} />,
    )
    const stroke = container.querySelector('path.line')
    expect(count(stroke?.getAttribute('d') ?? '', 'M')).toBe(2)
  })

  it('draws no path when all values are null', () => {
    const { container } = renderWithProviders(
      <LineChart series={[{ name: 'cpu', color: 'red', values: [null, null, null] }]} xLabels={['a', 'b']} />,
    )
    expect(container.querySelectorAll('path')).toHaveLength(0)
    expect(container.querySelectorAll('line').length).toBeGreaterThan(0)
  })
})

describe('LineChart marker labels', () => {
  const render = (at: number) =>
    renderWithProviders(<LineChart series={[{ name: 'cpu', color: 'red', values: [1, 2] }]} xLabels={['a', 'b']} marks={[{ at, label: 'deploy' }]} />)

  it('anchors a label near the right edge inward so it is not clipped', () => {
    const { getByText } = render(0.98)
    expect(getByText('deploy').style.transform).toBe('translateX(-100%)')
  })

  it('anchors a long label inward even when its marker is outside the edge zone', () => {
    const label = 'payments:1 notifications:2 billing:3'
    const { getByText } = renderWithProviders(
      <LineChart series={[{ name: 'cpu', color: 'red', values: [1, 2] }]} xLabels={['a', 'b']} marks={[{ at: 0.8, label }]} />,
    )
    expect(getByText(label).style.transform).toBe('translateX(-100%)')
  })

  it('anchors a label near the left edge inward', () => {
    const { getByText } = render(0.02)
    expect(getByText('deploy').style.transform).toBe('translateX(0)')
  })

  it('centres a label away from the edges', () => {
    const { getByText } = render(0.5)
    expect(getByText('deploy').style.transform).toBe('')
  })
})

describe('LineChart marker label collisions', () => {
  const series = [{ name: 'cpu', color: 'red', values: [1, 2] }]

  it('moves a label that would overlap its neighbour onto the second row', () => {
    const { getByText } = renderWithProviders(
      <LineChart series={series} xLabels={['a']} marks={[{ at: 0.5, label: 'notifications' }, { at: 0.52, label: 'auth:1' }]} />,
    )
    expect(getByText('notifications').style.top).toBe('-1px')
    expect(getByText('auth:1').style.top).toBe('17px')
  })

  it('keeps distant labels on the same row', () => {
    const { getByText } = renderWithProviders(
      <LineChart series={series} xLabels={['a']} marks={[{ at: 0.3, label: 'deploy' }, { at: 0.7, label: 'scale' }]} />,
    )
    expect(getByText('deploy').style.top).toBe('-1px')
    expect(getByText('scale').style.top).toBe('-1px')
  })

  it('drops a third overlapping label to a hover-only target with the full text', () => {
    const { queryByText, getByTitle } = renderWithProviders(
      <LineChart
        series={series}
        xLabels={['a']}
        marks={[{ at: 0.5, label: 'notifications' }, { at: 0.51, label: 'auth:1' }, { at: 0.52, label: 'billing:3' }]}
      />,
    )
    expect(queryByText('billing:3')).toBeNull()
    expect(getByTitle('billing:3')).toBeTruthy()
  })
})

describe('LineChart marker labels on a phone', () => {
  const marks = [
    { at: 0.3, label: 'notifications' },
    { at: 0.7, label: 'authentication' },
  ]
  const series = [{ name: 'cpu', color: 'red', values: [1, 2] }]
  let restoreMedia: () => void
  let originalRect: typeof HTMLElement.prototype.getBoundingClientRect

  beforeEach(() => {
    restoreMedia = installPhoneMedia(true)
    originalRect = HTMLElement.prototype.getBoundingClientRect
    HTMLElement.prototype.getBoundingClientRect = () => ({ left: 0, top: 0, width: 200, height: 100, right: 200, bottom: 100, x: 0, y: 0, toJSON: () => ({}) })
  })
  afterEach(() => {
    HTMLElement.prototype.getBoundingClientRect = originalRect
    restoreMedia()
  })

  it('places labels by the measured plot width, so labels that collide in 200 px use the second row', () => {
    const { getByText } = renderWithProviders(<LineChart series={series} xLabels={['a']} marks={marks} />)
    expect(getByText('notifications').style.top).toBe('-1px')
    expect(getByText('authentication').style.top).toBe('17px')
  })
})
