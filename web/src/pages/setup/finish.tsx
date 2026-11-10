import { Tabs } from 'radix-ui'
import { useQuery } from '@tanstack/react-query'
import { QRCodeSVG } from 'qrcode.react'
import { Button } from '@/components/ui/button'
import { Notice, Panel } from '@/components/ResourceUI'
import { CopyField } from '@/components/CopyField'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { SetupCertificate } from '@/components/Certificate'
import { api, APIError } from '@/lib/api'
import { checkQuery, usersQuery } from '@/lib/resources'
import { clients, platforms, clientImport } from '@/data/clients'
import type { UserLinks } from '@/types/api'
import { zhCN as t, subscriptionText as sub } from '@/i18n/zh-CN'

export function FinishStep({ complete }: { complete: () => void }) {
  const users = useQuery(usersQuery)
  const check = useQuery(checkQuery)
  const user = users.data?.find((item) => item.status === 'active')
  const links = useQuery({ queryKey: ['users', String(user?.id), 'links'], queryFn: ({ signal }) => api<UserLinks>(`/users/${user?.id}/links`, { signal }), enabled: !!user, gcTime: 0, staleTime: 0, retry: false })
  const data = !links.isFetching && !links.isError ? links.data : undefined
  return <>
    <p className="text-sm leading-6 text-muted-foreground">{t.setup.finishHint}</p>
    {check.data?.items.some((item) => item.id === 'platform') && <Notice>{t.common.devHint}</Notice>}
    {users.isPending ? <Loading /> : users.isError ? <ErrorNotice error={users.error} retry={() => { void users.refetch() }} /> : !user ? <Notice>{t.setup.noUser}</Notice> : <>
      {links.isFetching || links.isPending ? <Loading /> : links.isError ? <>
        {links.error instanceof APIError && links.error.code === 'subscription_unavailable' && <Notice>{t.setup.unavailable}</Notice>}
        <ErrorNotice error={links.error} retry={() => { void links.refetch() }} />
      </> : data && (data.nodes.length === 0 ? <Notice>{t.setup.noNodes}</Notice> : <>
        <div className="flex justify-center rounded-xl border bg-white p-4"><QRCodeSVG value={data.subscription.auto} size={256} marginSize={4} title={sub.qr} role="img" aria-label={sub.qr} className="h-auto max-w-full" /></div>
        <CopyField label={t.users.auto} value={data.subscription.auto} />
      </>)}
    </>}
    <Tabs.Root defaultValue="Windows" className="min-w-0 space-y-4">
      <Tabs.List aria-label={t.setup.clients} className="flex flex-wrap gap-2">{platforms.map((platform) => <Tabs.Trigger key={platform} value={platform} className="rounded-md border px-3 py-2 text-sm data-[state=active]:bg-primary data-[state=active]:text-primary-foreground">{platform}</Tabs.Trigger>)}</Tabs.List>
      {platforms.map((platform) => <Tabs.Content key={platform} value={platform} className="space-y-4">{clients.filter((client) => client.platforms.includes(platform)).map((client) => {
        const format = client.formats[0]
        const url = data?.nodes.length ? data.subscription[format] : undefined
        const link = url ? clientImport(client, url, user?.name ?? 'boxpanel') : undefined
        return <Panel key={client.name} title={client.name}>
          <div className="flex flex-wrap gap-3"><Button variant="outline" asChild><a href={client.download} target="_blank" rel="noreferrer">{t.setup.download}</a></Button><a href={client.source} target="_blank" rel="noreferrer" className="self-center text-sm underline">{t.setup.source}</a>{link && <Button asChild><a href={link} rel="noreferrer">{t.setup.import}</a></Button>}</div>
          {url && <CopyField label={t.users[format]} value={url} />}
          <ol className="list-inside list-decimal space-y-2 text-sm leading-6">{t.setup.installSteps.map((step) => <li key={step}>{step}</li>)}</ol>
        </Panel>
      })}</Tabs.Content>)}
    </Tabs.Root>
    <Notice>{sub.compatibility}</Notice><SetupCertificate />
    <Button onClick={complete}>{t.setup.enter}</Button>
  </>
}
