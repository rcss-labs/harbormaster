import type { Arm64Readiness } from '../../../api/deploy'
import { Banner } from '../../../components/Banner'

/** The sentence for one image the image check could not clear for ARM64. */
export function blockerText(image: Arm64Readiness['images'][number]): string {
  switch (image.reason) {
    case 'no_arm64_in_index':
      return `${image.image} has no arm64 build`
    case 'single_architecture':
      return `${image.image} is a single-architecture image`
    case 'pending':
      return `${image.image} has not been checked yet`
    case 'registry_not_checked':
      return `${image.image} is in a registry Harbormaster does not check`
    case 'no_permission':
      return `The harbormaster-access role in ${image.account} cannot read image manifests. Update the access module to the latest version.`
    case 'not_connected':
      return `Connect AWS account ${image.account} to check ${image.image}`
    case 'not_found':
      return `${image.image} was not found`
    default:
      return `${image.image} could not be read`
  }
}

interface Arm64NotesProps {
  readiness: Arm64Readiness | undefined
  /** The draft also changes an image tag. */
  tagChanged: boolean
}

/** Warnings for an ARM64 draft. They never block Review: the check can be a day old, and someone may know a tag was just rebuilt. */
export function Arm64Notes({ readiness, tagChanged }: Arm64NotesProps) {
  const blockers = readiness?.images.filter((image) => image.state !== 'arm64') ?? []
  return (
    <>
      {blockers.map((image) => (
        <Banner key={`${image.container}-${image.reason}`} kind={image.state === 'no_arm64' ? 'warn' : 'info'}>
          {blockerText(image)}
          {image.state === 'no_arm64' && ' '}
          {image.state === 'no_arm64' && <span>Tasks will fail to start on ARM64 until this image has an arm64 build.</span>}
        </Banner>
      ))}
      {tagChanged && <Banner kind="info">Changed image tags are not checked for arm64 until they are deployed.</Banner>}
    </>
  )
}
