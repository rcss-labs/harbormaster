import { Banner } from '../../components/Banner'

/** Shown on every tab when Harbormaster cannot change the service's deployment controller. */
export function ControllerBanner({ banner }: { banner: string }) {
  return <Banner kind="warn">{banner}</Banner>
}
