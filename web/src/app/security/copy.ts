// Every user-facing sentence of the Security page, so the wording lives in one place.
export const SECURITY_COPY = {
  crumbRoot: 'Estate',
  heading: 'Image security',
  dbUpdating: 'Updating vulnerability data',
  dbNone: 'No vulnerability data yet',
  dbBuilt: (ago: string) => `Vulnerability data built ${ago}`,
  scannerFailing: (since: string, lastError: string) => `The scanner has not succeeded since ${since}: ${lastError}`,
  dbStale: 'The vulnerability data is out of date. Scans wait for a refresh.',
  imageNotFound: 'This image is not available.',
  listFailed: 'The images could not be loaded.',
  noImages: 'No images have been requested yet.',
  noMatches: 'No images match.',
  searchPlaceholder: 'Search image, service or cluster',

  requestedLine: (by: string, service: string, source: string, ago: string) => `Requested by ${by} from ${service} (${source}) ${ago}`,
  scannedLine: (ago: string, syftVersion: string, builtAgo: string) => `Scanned ${ago} with Syft ${syftVersion} against data built ${builtAgo}`,
  packages: (count: number) => `${count} packages`,
  noFindings: 'No known vulnerabilities in the last scan.',
  noPackages: 'No OS or language packages found; nothing to match.',
  noFix: 'No fix yet',
  rescan: 'Rescan',
  rescanQueued: 'The image is queued for another scan.',

  acceptedUntil: (date: string) => `Accepted until ${date}`,
  acceptedForSome: (accepted: number, total: number) => `Accepted for ${accepted} of ${total} services`,
  acceptanceLine: (service: string, by: string, date: string, reason: string) => `${service}: ${by} until ${date}: ${reason}`,
  acceptRisk: 'Accept risk',
  revoke: 'Revoke',

  acceptTitle: (vulnId: string) => `Accept risk for ${vulnId}`,
  acceptServicesLegend: 'Services',
  acceptServicesNote: 'Other services that use this image still count it.',
  acceptReason: 'Reason',
  acceptDays: 'Days',
  acceptSubmit: (days: number) => `Accept for ${days} days`,
  acceptedToast: (vulnId: string, date: string, services: number) =>
    `${vulnId} accepted until ${date} for ${services} services. It returns to the list when that ends.`,
  revokeTitle: (vulnId: string, service: string) => `Revoke acceptance of ${vulnId} for ${service}`,
  revokeBody: (vulnId: string, service: string) => `${service} will count ${vulnId} again.`,
  revokedToast: (vulnId: string, service: string) => `${vulnId} is open again for ${service}.`,
} as const

export const SCAN_STATUS_LABEL = {
  queued: 'Queued',
  scanning: 'Scanning',
  scanned: 'Scanned',
  failed: 'Failed',
} as const
