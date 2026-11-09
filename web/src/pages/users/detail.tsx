import { useParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { QRCodeSVG } from 'qrcode.react'
import { Button } from '@/components/ui/button'
import { Page, Panel, Notice, Empty, BackLink } from '@/components/ResourceUI'
import { CopyField } from '@/components/CopyField'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { UserUsage, UserActions } from './components'
import { userQuery } from '@/lib/resources'
import { api } from '@/lib/api'
import type { UserLinks } from '@/types/api'
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
export function UserDetailPage() {
  const { id = '' } = useParams()
  const user = useQuery(userQuery(id))
  return <Page title={t.pages.userDetail} actions={<BackLink to="/users" />}>
    {user.isPending ? <Loading /> : user.isError ? <ErrorNotice error={user.error} retry={() => { void user.refetch() }} /> : <>
      <Panel title={user.data.name} hint={user.data.remark}><UserUsage user={user.data} /><UserActions user={user.data} detail /></Panel>
      {user.data.status !== 'active' && <Notice>{t.users.unavailable}</Notice>}
      <SubscriptionLinks key={id} id={id} />
    </>}
  </Page>
}
