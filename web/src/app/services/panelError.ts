import type { UseQueryResult } from '@tanstack/react-query'
import type { PanelErrorProps } from '../../components/PanelError'

/**
 * The Panel `error` prop for a query that failed with nothing to show: the message names what could not be read,
 * the raw error sits behind "Show error", and Retry refetches only this panel's request.
 */
export function queryPanelError(
  query: Pick<UseQueryResult, 'isError' | 'data' | 'error' | 'refetch'>,
  message: string,
): PanelErrorProps | undefined {
  if (!query.isError || query.data !== undefined) return undefined
  return {
    message,
    detail: query.error instanceof Error ? query.error.message : undefined,
    onRetry: () => void query.refetch(),
  }
}
