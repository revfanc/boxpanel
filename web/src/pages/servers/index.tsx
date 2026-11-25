import { useState } from 'react'
import { Link } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { LinkButton } from '@/components/LinkButton'
import { Empty, Page, Panel } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { serversQuery } from '@/lib/resources'
import { serverPath } from '@/lib/navigation'
import { zhCN as t } from '@/i18n/zh-CN'
import { JoinWizard } from './join-wizard'
import { ServerBadges } from './status-badges'

export function ServersPage() {
  const query = useQuery({ ...serversQuery, refetchInterval: 30_000 })
  const [adding, setAdding] = useState(false)
  const servers = query.data ?? []
  return <Page title={t.pages.servers} actions={<div className="flex flex-wrap gap-2">
    <LinkButton variant="outline"  render={<Link to="/inbounds" />}>{t.pages.inbounds}</LinkButton>
    <Button aria-expanded={adding} onClick={() => setAdding(!adding)}>{adding ? t.servers.closeAdd : t.servers.add}</Button>
  </div>}>
    {adding && <JoinWizard />}
    {query.isPending ? <Loading /> : query.isError && !query.data ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
      : servers.length === 0 ? <Empty>{t.servers.empty}</Empty> : <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        {servers.map((server) => <Panel key={server.id} title={server.name} hint={server.public_host || undefined}>
          <ServerBadges server={server} />
          <LinkButton variant="outline"  render={<Link to={serverPath(server.id)} />}
            aria-label={t.servers.detailsFor(server.name)}>{t.servers.viewDetails}</LinkButton>
        </Panel>)}
      </div>}
  </Page>
}
