import { useState } from 'react'
import { Link } from 'react-router'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Confirm, Empty, Field, Page, Panel, Submit } from '@/components/ResourceUI'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'
import { serverSchema } from '@/schemas/server'
import { api } from '@/lib/api'
import { bytes, duration, expiry, relativeTime } from '@/lib/format'
import { refreshResources, serversQuery } from '@/lib/resources'
import type { Server } from '@/types/api'
import { JoinWizard } from './join-wizard'

function EditServerForm({ server, close }: { server: Server; close: () => void }) {
  const client = useQueryClient()
  const operation = useOperation()
  const form = useForm({
    resolver: zodResolver(serverSchema),
    defaultValues: server.is_local ? { name: server.name } : { name: server.name, public_host: server.public_host },
  })
  return <form className="space-y-3" onSubmit={form.handleSubmit((body) => operation.run(
    () => api<Server>(`/servers/${server.id}`, { method: 'PUT', body }),
    async (updated) => {
      await client.cancelQueries({ queryKey: serversQuery.queryKey })
      client.setQueryData(serversQuery.queryKey, (current) => current?.map((row) => row.id === updated.id
        ? { ...row, ...updated } : row))
      close()
      if (updated.name !== server.name || updated.public_host !== server.public_host) {
        await Promise.all([
          client.invalidateQueries({ queryKey: ['users'] }),
          client.invalidateQueries({ queryKey: ['rules'] }),
        ])
      }
    },
  ))}>
    <Field label={t.servers.name} hint={t.servers.renameHint} error={form.formState.errors.name?.message} disabled={operation.pending} {...form.register('name')} />
    {!server.is_local && <Field label={t.servers.address} error={form.formState.errors.public_host?.message} disabled={operation.pending} {...form.register('public_host')} />}
    {operation.error != null && <ErrorNotice error={operation.error} />}
    <div className="flex flex-wrap gap-2">
      <Submit pending={operation.pending} />
      <Button type="button" variant="outline" disabled={operation.pending} onClick={close}>{t.common.cancel}</Button>
    </div>
  </form>
}

const syncErrors: Readonly<Record<string, string>> = t.servers.syncErrors
const online = 'border-emerald-500/35 bg-emerald-500/10 text-emerald-800 dark:text-emerald-300'
const statusColors: Record<Server['status'], string> = {
  local: online,
  online,
  offline: 'border-amber-500/35 bg-amber-500/10 text-amber-800 dark:text-amber-300',
  pending: 'border-border bg-muted text-muted-foreground',
}
const failed = 'border-red-500/35 bg-red-500/10 text-red-800 dark:text-red-300'

function ServerOverview({ servers }: { servers: Server[] }) {
  const stats = [
    [t.servers.total, servers.length],
    [t.servers.statuses.online, servers.filter((server) => server.status === 'online' || server.is_local).length],
    [t.servers.statuses.offline, servers.filter((server) => server.status === 'offline').length],
    [t.servers.syncStatuses.failed, servers.filter((server) => server.sync_status === 'failed').length],
  ] as const
  return <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
    {stats.map(([label, value]) => <div key={label} className="min-w-0 rounded-lg border bg-card px-4 py-3">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="text-xl font-semibold tabular-nums">{value}</dd>
    </div>)}
  </dl>
}

function ServerDetails({ server, multiple, masterVersion, now }: { server: Server; multiple: boolean; masterVersion?: string; now: number }) {
  const lastSeen = server.last_seen_at
  return <>
    <dl className="grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm [&>dt]:text-muted-foreground">
      <dt>{t.servers.address}</dt><dd>{server.public_host || t.common.unknown}</dd>
      <dt>{t.servers.status}</dt><dd>{multiple
        ? <Badge variant="outline" className={statusColors[server.status]}>{t.servers.statuses[server.status]}</Badge>
        : t.servers.statuses[server.status]}</dd>
      <dt>{t.servers.version}</dt><dd>{server.version || t.common.unknown}</dd>
      {multiple && server.inbound_counts && <>
        <dt>{t.servers.inboundCounts}</dt><dd>{server.inbound_counts.ready} / {server.inbound_counts.pending}</dd>
      </>}
      {!server.is_local && <>
        {server.sync_status && <>
          <dt>{t.servers.sync}</dt>
          <dd>{server.sync_status === 'failed' ? <Badge variant="outline" className={failed}>{t.servers.syncStatuses.failed}</Badge>
            : server.sync_status === 'syncing' && server.status !== 'online' ? t.servers.syncWaitingOnline
              : t.servers.syncStatuses[server.sync_status]}</dd>
          <dt>{t.servers.generations}</dt><dd>{server.applied_generation} / {server.desired_generation}</dd>
        </>}
        {server.singbox_active !== undefined && <>
          <dt>{t.servers.core}</dt><dd>{server.singbox_active ? t.servers.coreActive : t.servers.coreInactive}</dd>
        </>}
        <dt>{t.servers.lastSeen}</dt><dd>{lastSeen === null ? t.servers.noHeartbeat
          : <time dateTime={new Date(lastSeen * 1000).toISOString()} title={expiry(lastSeen)}>{relativeTime(lastSeen, now)}</time>}</dd>
        {server.uptime !== undefined && <>
          <dt>{t.servers.uptime}</dt><dd>{server.uptime < 60 ? t.servers.uptimeSeconds(server.uptime) : duration(server.uptime)}</dd>
        </>}
        {server.traffic_up !== undefined && server.traffic_down !== undefined && <>
          <dt>{t.servers.traffic}</dt><dd>{bytes(server.traffic_up)} / {bytes(server.traffic_down)}</dd>
        </>}
      </>}
    </dl>
    {!server.is_local && server.version && masterVersion && server.version !== masterVersion &&
      <p className="text-sm text-muted-foreground">{t.servers.versionDifference(masterVersion)}</p>}
    {server.sync_error && <p role="status" className="text-sm text-destructive">{syncErrors[server.sync_error] ?? t.servers.syncErrors.apply_failed}</p>}
    {!server.is_local && <p className="text-xs text-muted-foreground">{t.servers.keyRefresh}</p>}
  </>
}

export function ServersPage() {
  const client = useQueryClient()
  const query = useQuery({ ...serversQuery, refetchInterval: 30_000 })
  const operation = useOperation()
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<number | null>(null)
  const [removing, setRemoving] = useState<Server | null>(null)
  const servers = query.data ?? []
  const multiple = servers.length > 1
  const masterVersion = servers.find((server) => server.is_local)?.version
  return <Page title={t.pages.servers} actions={<Button aria-expanded={adding} onClick={() => setAdding(!adding)}>{adding ? t.servers.closeAdd : t.servers.add}</Button>}>
    {adding && <JoinWizard />}
    {query.isPending ? <Loading /> : query.isError ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
      : servers.length === 0 ? <Empty>{t.servers.empty}</Empty> : <>
        {multiple && <ServerOverview servers={servers} />}
        <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        {servers.map((server) => <Panel key={server.id} title={server.name}>
          <ServerDetails server={server} multiple={multiple} masterVersion={masterVersion} now={query.dataUpdatedAt} />
          {editing === server.id ? <EditServerForm server={server} close={() => setEditing(null)} />
            : <div className="flex flex-wrap gap-2">
              {multiple && <Button variant="outline" asChild><Link to={`/inbounds?server=${server.id}`}>{t.servers.viewInbounds}</Link></Button>}
              <Button variant="outline" disabled={editing !== null} onClick={() => setEditing(server.id)}>{server.is_local ? t.servers.rename : t.common.edit}</Button>
              {!server.is_local && <Button variant="outline" disabled={editing !== null} onClick={() => { operation.reset(); setRemoving(server) }}>{t.servers.remove}</Button>}
            </div>}
        </Panel>)}
        </div>
      </>}
    {removing && <Confirm open onOpenChange={(open) => { if (!open) setRemoving(null) }}
      title={t.servers.remove} description={t.servers.removeHint(removing.name)} pending={operation.pending} error={operation.error} onConfirm={() => {
        void operation.run(() => api<void>(`/servers/${removing.id}`, { method: 'DELETE' }), async () => {
          await refreshResources(client)
          setRemoving(null)
        })
      }} />}
  </Page>
}
