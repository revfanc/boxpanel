import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { ServerBilling } from '@/components/AlertOverview'
import { Empty, Panel } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { serverTrafficQuery } from '@/lib/resources'
import { bytes } from '@/lib/format'
import { zhCN as t } from '@/i18n/zh-CN'
import type { Server } from '@/types/api'

export function ServerTrafficTab({ server }: { server: Server }) {
  const query = useQuery(serverTrafficQuery(server.id))
  if (query.isPending) return <Loading />
  if (query.isError && !query.data) return <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
  const { billing, user_traffic: usage, users } = query.data
  return <>
    {query.isError && <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />}
    <div className="grid min-w-0 gap-4 xl:grid-cols-2">
      <ServerBilling billing={billing} />
      <Panel title={t.servers.userTraffic} hint={t.servers.trafficHint}>
        <p className="text-2xl font-semibold tabular-nums">{bytes(usage.up + usage.down)}</p>
        <p className="text-sm text-muted-foreground">{t.users.upload} {bytes(usage.up)} · {t.users.download} {bytes(usage.down)}</p>
        {server.is_local && <p className="text-sm text-muted-foreground">{t.users.trafficByServerHint}</p>}
      </Panel>
    </div>
    <Panel title={t.users.usageByUser}>
      {users.length === 0 ? <Empty>{t.servers.noTraffic}</Empty> :
        <table className="w-full table-fixed text-sm" aria-label={t.users.usageByUser}>
          <thead className="border-b text-muted-foreground">
            <tr><th scope="col" className="w-2/5 py-2 text-left font-medium">{t.users.name}</th>
              <th scope="col" className="w-[30%] py-2 text-right font-medium">{t.users.upload}</th>
              <th scope="col" className="w-[30%] py-2 text-right font-medium">{t.users.download}</th></tr>
          </thead>
          <tbody className="divide-y">
            {users.map((row) => <tr key={row.id}>
              <th scope="row" className="py-2 pr-2 text-left font-medium"><Link to={`/users/${row.id}?tab=traffic`} className="underline-offset-4 hover:underline">{row.name}</Link></th>
              <td className="py-2 text-right tabular-nums">{bytes(row.up)}</td>
              <td className="py-2 text-right tabular-nums">{bytes(row.down)}</td>
            </tr>)}
          </tbody>
        </table>}
    </Panel>
  </>
}
