import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Empty, Page } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { serversQuery } from '@/lib/resources'
import { expiry } from '@/lib/format'
import { zhCN as t } from '@/i18n/zh-CN'
import { JoinWizard } from './join-wizard'
import { ServerTable } from './server-table'

export function ServersPage() {
  const query = useQuery({ ...serversQuery, refetchInterval: 30_000 })
  const [adding, setAdding] = useState(false)
  const servers = query.data ?? []
  return <Page title={t.pages.servers} actions={<Button aria-expanded={adding} onClick={() => setAdding(!adding)}>{adding ? t.servers.closeAdd : t.servers.add}</Button>}>
    {adding && <JoinWizard />}
    {query.isPending ? <Loading /> : query.isError && !query.data ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
      : servers.length === 0 ? <Empty>{t.servers.empty}</Empty> : <ServerTable servers={servers} columns={[t.servers.version, t.servers.lastSeen]}>
        {(server) => <>
          <td>{server.version || t.common.unknown}</td>
          <td>{server.is_local ? t.common.unknown : server.last_seen_at ? expiry(server.last_seen_at) : t.servers.noHeartbeat}</td>
        </>}
      </ServerTable>}
  </Page>
}
