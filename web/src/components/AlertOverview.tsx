import { Link } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Notice, Panel } from '@/components/ResourceUI'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { alertOverviewQuery, billingGB, utcDate } from '@/lib/alerts'
import { api } from '@/lib/api'
import { bytes, duration } from '@/lib/format'
import { zhCN as t } from '@/i18n/zh-CN'
import { useOperation } from '@/hooks/use-operation'
import type { AlertOverview as Overview, AlertEvent } from '@/types/api'

const a = t.alerts

export function ServerBilling({ billing, showSettingsLink = true }: { billing: Overview['billing']; showSettingsLink?: boolean }) {
  return <Panel title={a.billing} hint={a.accounting}>
    {billing.supported ? <>
      {billing.sampled_at > 0 ? <>
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <p className="text-2xl font-semibold tabular-nums">{billingGB(billing.used)}
            <span className="ml-2 text-sm font-normal text-muted-foreground">{billing.quota > 0 ? `/ ${billingGB(billing.quota)}` : a.noQuota}</span>
          </p>
          {billing.quota > 0 && <span className="font-medium tabular-nums">{billing.percent.toFixed(1)}%</span>}
        </div>
        {billing.quota > 0 && <Progress aria-label={a.billing} value={Math.min(100, billing.percent)} />}
        <p className="text-sm text-muted-foreground">{a.sample(billing.interface, utcDate(billing.sampled_at))}</p>
        {billing.stale && <Notice>{a.staleSample}</Notice>}
      </> : <Notice>{a.collecting}</Notice>}
      <p className="text-sm text-muted-foreground">{a.cycle(utcDate(billing.starts_at), utcDate(billing.ends_at))} · {a.modes[billing.billing_mode]}</p>
    </> : <Notice>{a.unsupported}</Notice>}
    {showSettingsLink && <Button asChild variant="outline" size="sm"><Link to="/settings">{a.configurable}</Link></Button>}
  </Panel>
}

function UnreadEvent({ event }: { event: AlertEvent }) {
  const read = useOperation()
  const client = useQueryClient()
  const format = event.kind.startsWith('server_quota') ? billingGB : bytes
  return <li className="space-y-2 border-b py-3 last:border-0">
    <div className="flex flex-wrap items-start justify-between gap-2">
      <div>
        <p className="font-medium">{a.event[event.kind]}{event.username && ` · ${event.username}`}</p>
        <p className="text-xs text-muted-foreground">{event.address || a.server} · {utcDate(event.created_at)}</p>
      </div>
      <Button type="button" variant="outline" size="sm" disabled={read.pending} onClick={() => {
        void read.run(() => api(`/alerts/events/${event.key}/read`, { method: 'POST' }), () => client.invalidateQueries({ queryKey: alertOverviewQuery.queryKey }))
      }}>{a.markRead}</Button>
    </div>
    {event.limit > 0 && <p className="text-sm">{a.quotaUsage(format(event.used), format(event.limit), (100 * event.used / event.limit).toFixed(1))}</p>}
    {!!event.deadline && <p className="text-sm">{a.deadline(utcDate(event.deadline))}</p>}
    {!!event.offline_seconds && <p className="text-sm">{a.offlineDuration(duration(event.offline_seconds))}</p>}
    {read.error != null && <ErrorNotice error={read.error} />}
  </li>
}

function NotificationHistory({ recent }: { recent: Overview['recent'] }) {
  return <Panel title={a.notificationHistory}>
    {recent.length === 0 ? <p className="text-sm text-muted-foreground">{a.emptyHistory}</p> : <ul className="max-h-96 overflow-y-auto">
      {recent.map((row) => <li className="space-y-1 border-b py-3 last:border-0" key={row.id}>
        <p className="font-medium">{row.kind === 'test' ? a.test : a.event[row.kind]}{row.username && ` · ${row.username}`}</p>
        <p className="text-sm text-muted-foreground">{a.channelNames[row.channel]} · {a.status[row.status]}{row.grouped && ` · ${a.grouped}`}</p>
        <p className="text-xs text-muted-foreground">{utcDate(row.updated_at)} · {a.attempts(row.attempts)}</p>
        {row.error && <p className="text-sm">{a.failure[row.error as keyof typeof a.failure] ?? a.unknownFailure}</p>}
      </li>)}
    </ul>}
  </Panel>
}

export function AlertOverviewPanels() {
  const query = useQuery(alertOverviewQuery)
  if (query.isPending) return <Panel title={a.billing}><Loading /></Panel>
  if (query.isError && !query.data) return <Panel title={a.inbox}><ErrorNotice error={query.error} retry={() => { void query.refetch() }} /></Panel>
  const overview = query.data
  return <>
    {query.isError && <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />}
    <ServerBilling billing={overview.billing} />
    <div className="grid min-w-0 items-start gap-4 lg:grid-cols-2">
      <Panel title={a.inbox} hint={a.unreadCount(overview.unread_count)}>
        {overview.unread.length === 0 ? <p className="text-sm text-muted-foreground">{a.emptyInbox}</p> : <ul className="max-h-96 overflow-y-auto">
          {overview.unread.map((event) => <UnreadEvent key={event.key} event={event} />)}
        </ul>}
      </Panel>
      <NotificationHistory recent={overview.recent} />
    </div>
  </>
}
