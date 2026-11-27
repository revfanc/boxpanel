import { useState } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { LinkButton } from '@/components/LinkButton'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import { ChoiceSelect } from '@/components/ChoiceSelect'
import { Page, Empty, Confirm, Notice } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { inboundsQuery, refreshResources, serversQuery } from '@/lib/resources'
import { api } from '@/lib/api'
import { serverID, serverPath } from '@/lib/navigation'
import { useOperation } from '@/hooks/use-operation'
import type { Inbound, Server } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export function InboundsPage() {
  const [params] = useSearchParams()
  const legacy = serverID(params.get('server'))
  return legacy ? <Navigate to={serverPath(legacy, 'inbounds')} replace /> : <AllInbounds />
}

function AllInbounds() {
  const servers = useQuery(serversQuery)
  const [params, setParams] = useSearchParams()
  const serverList = servers.data ?? []
  const selected = serverList.find(({ id }) => id === serverID(params.get('filter')))
  const unresolvedFilter = params.has('filter') && !selected
  const options = [{ value: 'all', label: t.inbounds.allServers },
    ...serverList.map((server) => ({ value: String(server.id), label: server.name }))]
  return <Page title={t.pages.inbounds} description={t.inbounds.description}
    actions={!unresolvedFilter && selected?.status !== 'pending' && <NewInboundLink server={selected} />}>
    {serverList.length > 1 && !unresolvedFilter && <div className="flex flex-wrap items-center gap-3">
      <Label htmlFor="server-filter">{t.inbounds.server}</Label>
      <ChoiceSelect id="server-filter" className="w-full min-w-0 sm:w-fit sm:max-w-sm"
        value={selected ? String(selected.id) : 'all'} options={options} onChange={(value) => {
          const next = new URLSearchParams(params)
          if (value === 'all') next.delete('filter')
          else next.set('filter', value)
          setParams(next)
        }} />
    </div>}
    {servers.isError && <ErrorNotice error={servers.error} retry={() => { void servers.refetch() }} />}
    {unresolvedFilter ? servers.isPending ? <Loading /> : <Empty>
      <p>{t.inbounds.filterUnavailable}</p>
      <Link className="underline underline-offset-4" to="/inbounds">{t.inbounds.allServers}</Link>
    </Empty> : <InboundList servers={serverList} filter={selected?.id} />}
  </Page>
}

function NewInboundLink({ server, scoped = false }: { server?: Server; scoped?: boolean }) {
  return <LinkButton render={<Link to={server ? `/inbounds/new?${scoped ? 'server' : 'filter'}=${server.id}` : '/inbounds/new'} />}>{t.pages.newInbound}</LinkButton>
}

export function ServerInbounds({ server }: { server: Server }) {
  return <>
    {server.status !== 'pending' && <NewInboundLink server={server} scoped />}
    <InboundList servers={[server]} filter={server.id} scoped />
    {!server.is_local && (server.inbound_counts?.ready ?? 0) > 0 &&
      <p className="text-sm text-muted-foreground">{t.servers.keyRefresh}</p>}
  </>
}

function InboundList({ servers, filter, scoped = false }: { servers: Server[]; filter?: number; scoped?: boolean }) {
  const query = useQuery({ ...inboundsQuery, refetchInterval: (query) =>
    query.state.data?.some((inbound) => inbound.key_status === 'pending' && (!filter || inbound.server_id === filter)) ? 5000 : false })
  const visible = query.data?.filter((inbound) => !filter || inbound.server_id === filter) ?? []
  const client = useQueryClient()
  const operation = useOperation()
  const [action, setAction] = useState<{ node: Inbound; kind: 'delete' | 'disable' } | null>(null)
  function toggle(node: Inbound) {
    void operation.run(() => api<Inbound>(`/inbounds/${node.id}/toggle`, { method: 'POST', body: { enabled: !node.enabled } }), async () => { setAction(null); await refreshResources(client) })
  }
  return <>
    {operation.error != null && !action && <ErrorNotice error={operation.error} />}
    {query.isPending ? <Loading /> : query.isError ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} /> : visible.length === 0 ? <Empty>{filter ? t.inbounds.serverEmpty : t.inbounds.empty}</Empty> : <div className="min-w-0 overflow-x-auto rounded-xl border">
      <table className="w-full text-left text-sm" aria-label={t.pages.inbounds}>
        <thead className="border-b bg-muted/40 text-muted-foreground"><tr>
          {[t.inbounds.name, ...(scoped ? [] : [t.inbounds.server]), t.inbounds.protocol, t.inbounds.port, t.servers.tabs.status, t.inbounds.actions].map((label) =>
            <th scope="col" key={label} className="whitespace-nowrap px-4 py-3 font-medium">{label}</th>)}
        </tr></thead>
        <tbody className="divide-y">{visible.map((node) => {
          const server = servers.find(({ id }) => id === node.server_id)
          const context = scoped ? `?server=${node.server_id}` : filter ? `?filter=${filter}` : ''
          return <tr key={node.id} className="align-top [&>td]:px-4 [&>td]:py-3">
            <th scope="row" className="min-w-32 px-4 py-3 font-medium">{node.remark || node.tag}
              {node.remark && node.tag !== node.remark && <div className="text-xs font-normal text-muted-foreground">{node.tag}</div>}
            </th>
            {!scoped && <td>{server ? <Link className="underline-offset-4 hover:underline" to={serverPath(server.id, 'inbounds')}>{server.name}</Link> : t.common.unknown}</td>}
            <td>{node.protocol === 'vless_reality' ? t.inbounds.reality : t.inbounds.hysteria2}</td>
            <td className="whitespace-nowrap tabular-nums">{node.port} / {node.protocol === 'vless_reality' ? 'TCP' : 'UDP'}</td>
            <td><Badge variant={node.enabled ? 'default' : 'outline'}>{node.enabled ? t.common.enabled : t.common.disabled}</Badge>
              {node.warning && <Notice>{node.warning}</Notice>}
              {node.key_status === 'pending' && <p className="mt-1 text-xs text-muted-foreground">{t.inbounds.pendingKeys}</p>}
              {!node.enabled && <p className="mt-1 text-xs text-muted-foreground">{node.key_status ? t.inbounds.remoteApply : t.common.applyHint}</p>}
            </td>
            <td><div className="flex flex-nowrap gap-2">
              <LinkButton variant="outline" size="sm" render={<Link to={`/inbounds/${node.id}${context}`} />}>{t.common.edit}</LinkButton>
              <Button variant="outline" size="sm" disabled={operation.pending || (!!node.key_status && !server)} onClick={() => { operation.reset(); if (node.enabled) setAction({ node, kind: 'disable' }); else toggle(node) }}>{operation.pending ? t.common.working : node.enabled ? t.common.disable : t.common.enable}</Button>
              <Button variant="ghost" size="sm" className="text-destructive" disabled={operation.pending} onClick={() => { operation.reset(); setAction({ node, kind: 'delete' }) }}>{t.common.delete}</Button>
            </div></td>
          </tr>
        })}</tbody>
      </table>
    </div>}
    <Confirm open={!!action} onOpenChange={(open) => { if (!open) setAction(null) }} title={action?.kind === 'delete' ? t.inbounds.deleteTitle : t.common.disable} description={<>{action?.node.remark || action?.node.tag}<br />{action?.kind === 'delete' ? t.inbounds.deleteHint : t.common.disableHint}{action?.node.enabled && <><br />{action.node.key_status ? t.inbounds.remoteApply : t.common.applyHint}</>}</>} pending={operation.pending} error={operation.error} onConfirm={() => {
      if (!action) return
      if (action.kind === 'disable') toggle(action.node)
      else void operation.run(() => api<void>(`/inbounds/${action.node.id}`, { method: 'DELETE' }), async () => { setAction(null); await refreshResources(client) })
    }} />
  </>
}
