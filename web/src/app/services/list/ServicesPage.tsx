import { EmptyState } from '../../../components/EmptyState'

/** Placeholder so the /services route exists in wave 0; lane 1D replaces it with the real page. */
export function ServicesPage() {
  return (
    <>
      <h1>Services</h1>
      <EmptyState>Loading services.</EmptyState>
    </>
  )
}
