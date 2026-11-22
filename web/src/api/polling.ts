/** Polling cadences shared by every query hook. None of them runs while the tab is in the background. */
export const LIVE_REFETCH_MS = 5_000
export const METRICS_REFETCH_MS = 60_000

/** Live data (status, tasks, snapshots) refreshes every 5 s. */
export const livePolling = { refetchInterval: LIVE_REFETCH_MS, refetchIntervalInBackground: false } as const

/** CloudWatch-backed metrics move slowly: refresh every minute. */
export const metricsPolling = { refetchInterval: METRICS_REFETCH_MS, refetchIntervalInBackground: false } as const
