import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { AlertOverviewPanels } from '@/components/AlertOverview'
import { Page, Panel } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Progress } from '@/components/ui/progress'
import { ServerTable } from '@/pages/servers/server-table'
import { overviewQuery } from '@/lib/resources'
import { bytes, expiry, metricRates, metricUsage } from '@/lib/format'
import { billingGB } from '@/lib/alerts'
import type { OverviewServer, PanelOverview, TrafficUser } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

function MetricCells({ server }: { server: OverviewServer }) {
  const metrics = server.system.metrics
  const available = metrics?.supported
  const billing = server.billing
  const hasSample = billing.supported && billing.sampled_at > 0
  return <>
    <td>
      {available ? `${metrics.cpu.toFixed(1)}%` : t.common.unknown}
      {!server.is_local && !metrics && <p className="text-xs text-muted-foreground">
        {server.last_seen_at === null ? t.servers.noHeartbeat : t.servers.metricsUpgrade}
      </p>}
    </td>
    <td>{available ? metricUsage(metrics.mem) : t.common.unknown}</td>
    <td>{available ? metricRates(metrics.net) : t.common.unknown}</td>
    <td>
      <p className="whitespace-nowrap">{bytes(server.user_traffic.up + server.user_traffic.down)} · {server.share_percent.toFixed(1)}%</p>
      <Progress className="mt-1" value={Math.min(100, server.share_percent)} aria-hidden="true" />
    </td>
    <td>
      <p className="whitespace-nowrap">
        {hasSample ? billingGB(billing.used) : billing.supported ? t.dashboard.waitingBilling : t.common.unknown}
        {billing.quota > 0 && <span> / {billingGB(billing.quota)}{hasSample && ` · ${billing.percent.toFixed(1)}%`}</span>}
      </p>
      {hasSample && billing.stale && <p className="text-xs text-muted-foreground">{t.dashboard.stale}</p>}
      {hasSample && billing.quota === 0 && <p className="text-xs text-muted-foreground">{t.alerts.noQuota}</p>}
    </td>
  </>
}

function UserList({ title, users, value }: { title: string; users: TrafficUser[]; value: (user: TrafficUser) => string }) {
  return <Panel title={title}>
    {users.length === 0 ? <p className="text-sm text-muted-foreground">{t.dashboard.noUsers}</p> : <ul className="divide-y">
      {users.map((user) => <li key={user.id} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
        <Link to={`/users/${user.id}?tab=traffic`} className="font-medium underline-offset-4 hover:underline">{user.name}</Link>
        <span className="text-muted-foreground tabular-nums">{value(user)}</span>
      </li>)}
    </ul>}
  </Panel>
}

function OverviewContent({ overview }: { overview: PanelOverview }) {
  const usage = overview.user_traffic
  return <>
    <Panel title={t.dashboard.totalUsage} hint={t.dashboard.usageHint}>
      <p className="text-3xl font-semibold tabular-nums">{bytes(usage.up + usage.down)}</p>
      <p className="text-sm text-muted-foreground">{t.users.upload} {bytes(usage.up)} · {t.users.download} {bytes(usage.down)}</p>
    </Panel>
    <Panel title={t.pages.servers}>
      <ServerTable servers={overview.servers} columns={[t.dashboard.cpu, t.dashboard.memory, t.dashboard.network, t.dashboard.serverShare, t.alerts.billing]}>
        {(server) => <MetricCells server={server} />}
      </ServerTable>
    </Panel>
    <div className="grid min-w-0 gap-4 lg:grid-cols-3">
      <UserList title={t.dashboard.highestUsage} users={overview.top_users} value={(user) => bytes(user.up + user.down)} />
      <UserList title={t.dashboard.expiring} users={overview.expiring_users} value={(user) => expiry(user.expire_at)} />
      <UserList title={t.dashboard.exhausted} users={overview.exhausted_users} value={(user) => `${bytes(user.up + user.down)} / ${bytes(user.traffic_limit)}`} />
    </div>
    <AlertOverviewPanels overview={overview} />
  </>
}

export function DashboardPage() {
  const query = useQuery(overviewQuery)
  return <Page title={t.pages.home}>
    {query.isPending && <Loading />}
    {query.isError && <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />}
    {query.data && <OverviewContent overview={query.data} />}
  </Page>
}
