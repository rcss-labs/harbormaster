import { usePollerStatus } from '../../api/hooks'
import { Banner } from '../../components/Banner'

const minutesSince = (iso: string) => Math.max(1, Math.floor((Date.now() - Date.parse(iso)) / 60_000))

export function PollerBanners() {
  const { data } = usePollerStatus()
  return (
    <>
      {(data?.items ?? []).map((entry) => {
        const key = `${entry.account_id}/${entry.region}`
        const where = (
          <em>
            {entry.account_name} / {entry.region}
          </em>
        )
        return (
          <div key={key} style={{ display: 'contents' }}>
            {entry.failing && (
              <Banner kind="bad">
                Data for {where} is {entry.last_ok_at ? `${minutesSince(entry.last_ok_at)} min old` : 'unavailable'}: {entry.last_error}
              </Banner>
            )}
            {entry.throttled && <Banner kind="warn">Updates for {where} are slowed by AWS throttling</Banner>}
          </div>
        )
      })}
    </>
  )
}
