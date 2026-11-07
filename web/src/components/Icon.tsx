import { ICONS, type IconName } from './icons'

interface IconProps {
  name: IconName
  size?: 12 | 14 | 16 | 18
  /** Accessible name. Without it the icon is decorative and hidden from assistive tech. */
  label?: string
  className?: string
}

/** Inline 24-viewBox stroke SVG from the v4 icon set; colour follows `currentColor`. */
export function Icon({ name, size = 16, label, className }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.75}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      {...(label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true })}
    >
      <path d={ICONS[name]} />
    </svg>
  )
}
