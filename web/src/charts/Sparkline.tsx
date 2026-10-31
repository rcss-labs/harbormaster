interface SparklineProps {
  values: number[]
  color?: string
  /** Pixel height, clamped to 24–30. */
  height?: number
  /** Stretch to the container width instead of the fixed 110 px. */
  fluid?: boolean
}

const WIDTH = 110

/** Mini line with a faint area fill and an end dot (mockup spark()). */
export function Sparkline({ values, color = 'var(--accent)', height = 26, fluid = false }: SparklineProps) {
  const h = Math.min(30, Math.max(24, height))
  const w = WIDTH
  const max = Math.max(10, ...values)
  const pts = values.map((v, i) => [
    values.length > 1 ? i * (w / (values.length - 1)) : 0,
    h - 2 - (v / max) * (h - 4),
  ])
  const line = pts.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' ')
  const end = pts[pts.length - 1]
  return (
    <svg
      width={fluid ? '100%' : w}
      height={h}
      viewBox={`0 0 ${w} ${h}`}
      preserveAspectRatio={fluid ? 'none' : undefined}
      aria-hidden="true"
      style={{ display: 'block' }}
    >
      <path d={`${line} L ${w} ${h} L 0 ${h} Z`} fill={color} opacity={0.13} />
      <path d={line} fill="none" stroke={color} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
      {!fluid && end && <circle cx={end[0]} cy={end[1]} r={2.2} fill={color} />}
    </svg>
  )
}
