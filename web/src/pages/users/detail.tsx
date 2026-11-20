import { useParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { QRCodeSVG } from 'qrcode.react'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { Page, Panel, Notice, Empty, BackLink } from '@/components/ResourceUI'
import { CopyField } from '@/components/CopyField'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { UserUsage, UserActions } from './components'
import { userQuery } from '@/lib/resources'
import { api } from '@/lib/api'
import { bytes } from '@/lib/format'
import type { User, UserLinks } from '@/types/api'
import { zhCN as t, subscriptionText as sub } from '@/i18n/zh-CN'

function SubscriptionLinks({ id }: { id: string }) {
  const links = useQuery({ queryKey: ['users', id, 'links'], queryFn: ({ signal }) => api<UserLinks>(`/users/${id}/links`, { signal }), gcTime: 0, staleTime: 0 })
  return <Panel title={t.pages.userDetail} hint={sub.privacy}>
    <Button variant="outline" disabled={links.isFetching} onClick={() => { void links.refetch() }}>{t.common.refresh}</Button>
    {links.isFetching || links.isPending ? <Loading /> : links.isError ? <ErrorNotice error={links.error} retry={() => { void links.refetch() }} /> : <>
      <div className="flex justify-center rounded-lg bg-white p-3"><QRCodeSVG value={links.data.subscription.auto} size={224} marginSize={4} title={sub.qr} role="img" aria-label={sub.qr} className="h-auto max-w-full" /></div>
      <p className="text-sm leading-6 text-muted-foreground">{sub.scan}</p>
      <div className="flex flex-wrap gap-2"><Button asChild variant="outline" className="h-auto whitespace-normal py-2"><a href={links.data.import.singbox} rel="noreferrer">{sub.singboxImport}</a></Button><Button asChild variant="outline" className="h-auto whitespace-normal py-2"><a href={links.data.import.clash} rel="noreferrer">{sub.clashImport}</a></Button></div>
      {(['auto', 'singbox', 'clash', 'base64'] as const).map((format) => <CopyField key={links.data.subscription[format]} label={t.users[format]} value={links.data.subscription[format]} />)}
      <Notice>{sub.compatibility}</Notice>
      <h2 className="pt-3 font-medium">{t.users.nodes}</h2>
      {links.data.nodes.length === 0 ? <Empty>{t.users.noNodes}</Empty> : links.data.nodes.map((node) => <CopyField key={`${node.inbound_id}-${node.uri}`} label={node.remark || `#${node.inbound_id}`} value={node.uri} />)}
    </>}
  </Panel>
}

const percent = new Intl.NumberFormat('zh-CN', { style: 'percent', maximumFractionDigits: 1 })

function TrafficByServer({ user }: { user: User }) {
  if (!user.traffic_by_server?.length) return null
  const total = user.traffic_up + user.traffic_down
  return <Panel title={t.users.trafficByServer} hint={t.users.trafficByServerHint}>
    <table className="w-full table-fixed text-sm" aria-label={t.users.trafficByServer}>
      <thead className="border-b text-muted-foreground">
        <tr>
          <th scope="col" className="w-[34%] pb-2 text-left font-medium">{t.pages.servers}</th>
          <th scope="col" className="w-[20%] pb-2 text-right font-medium">{t.users.upload}</th>
          <th scope="col" className="w-[20%] pb-2 text-right font-medium">{t.users.download}</th>
          <th scope="col" className="w-[26%] pb-2 pl-3 text-right font-medium">{t.users.share}</th>
        </tr>
      </thead>
      <tbody className="divide-y">
        {user.traffic_by_server.map((row) => {
          const share = total > 0 ? Math.min(1, (row.up + row.down) / total) : 0
          return <tr key={row.server_id} className="align-top">
            <th scope="row" className="py-2 pr-1 text-left font-medium">{row.name}{' '}
              {row.is_local && row.name !== t.users.localAndUnattributed && <span className="block text-xs font-normal text-muted-foreground">{t.users.localAndUnattributed}</span>}
            </th>
            <td className="py-2 pl-1 text-right tabular-nums">{bytes(row.up)}</td>
            <td className="py-2 pl-1 text-right tabular-nums">{bytes(row.down)}</td>
            <td className="space-y-1 py-2 pl-3 text-right tabular-nums">
              <span>{percent.format(share)}</span><Progress value={share * 100} aria-hidden="true" />
            </td>
          </tr>
        })}
      </tbody>
    </table>
  </Panel>
}

export function UserDetailPage() {
  const { id = '' } = useParams()
  const user = useQuery(userQuery(id))
  return <Page title={t.pages.userDetail} actions={<BackLink to="/users" />}>
    {user.isPending ? <Loading /> : user.isError ? <ErrorNotice error={user.error} retry={() => { void user.refetch() }} /> : <>
      <Panel title={user.data.name} hint={user.data.remark}><UserUsage user={user.data} /><UserActions user={user.data} detail /></Panel>
      <TrafficByServer user={user.data} />
      {user.data.status !== 'active' && <Notice>{t.users.unavailable}</Notice>}
      <SubscriptionLinks key={id} id={id} />
    </>}
  </Page>
}
