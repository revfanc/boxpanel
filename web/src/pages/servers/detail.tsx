import { useParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { ServerAlertPanel } from '@/components/AlertSettings'
import { BackLink, Page } from '@/components/ResourceUI'
import { PageTabs } from '@/components/PageTabs'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { NotFoundPage } from '@/pages/not-found'
import { ServerInbounds } from '@/pages/inbounds'
import { serverID } from '@/lib/navigation'
import { serverQuery } from '@/lib/resources'
import { zhCN as t } from '@/i18n/zh-CN'
import { ServerStatusTab } from './status'
import { ServerTrafficTab } from './traffic'
import { ServerLogsTab } from './logs'
import { ServerExitTab } from './exit'

function ServerDetail({ id }: { id: number }) {
  const query = useQuery(serverQuery(id))
  const server = query.data
  return <Page title={server?.name ?? t.servers.detail} description={server?.public_host || undefined} actions={<BackLink to="/servers" />}>
    {query.isPending ? <Loading /> : query.isError && !server ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} /> : server && <>
      {query.isError && <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />}
      <PageTabs key={id} label={t.servers.detail} tabs={[
        { id: 'status', label: t.servers.tabs.status, content: <ServerStatusTab server={server} /> },
        { id: 'inbounds', label: t.servers.tabs.inbounds, content: <ServerInbounds server={server} /> },
        { id: 'exit', label: t.servers.tabs.exit, content: <ServerExitTab serverID={id} /> },
        { id: 'traffic', label: t.servers.tabs.traffic, content: <ServerTrafficTab server={server} /> },
        { id: 'alerts', label: t.servers.tabs.alerts, content: <ServerAlertPanel serverID={id} /> },
        { id: 'logs', label: t.servers.tabs.logs, content: <ServerLogsTab server={server} /> },
      ]} />
    </>}
  </Page>
}

export function ServerDetailPage() {
  const id = serverID(useParams().id)
  return id ? <ServerDetail key={id} id={id} /> : <NotFoundPage />
}
