import { EmptyState } from '../../components/EmptyState'

/** Stand-in for tabs built by a later plan; `owner` (the plan that replaces it) is kept as a data attribute, not shown. */
export function PlaceholderTab({ owner }: { owner: '01' | '02' | '03' | '04' }) {
  return (
    <div data-owner={owner}>
      <EmptyState>This tab arrives in a later phase.</EmptyState>
    </div>
  )
}
