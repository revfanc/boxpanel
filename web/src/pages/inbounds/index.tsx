import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Page, Panel, Empty, Confirm } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { inboundsQuery, refreshResources, serversQuery } from '@/lib/resources'
import { api } from '@/lib/api'
import { useOperation } from '@/hooks/use-operation'
import type { Inbound } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export function InboundsPage() {
  const query = useQuery({ ...inboundsQuery, refetchInterval: (query) =>
    query.state.data?.some((node) => node.key_status === 'pending') ? 5000 : false })
  const servers = useQuery(serversQuery)
  const [params, setParams] = useSearchParams()
  const serverList = servers.data ?? []
  const selected = serverList.find(({ id }) => String(id) === params.get('server'))
  const visibleNodes = query.data?.filter((node) => !selected || node.server_id === selected.id) ?? []
  const canCreateOnSelected = selected && selected.status !== 'pending'
  const client = useQueryClient()
  const operation = useOperation()
  const [action, setAction] = useState<{ node: Inbound; kind: 'delete' | 'disable' } | null>(null)
  function toggle(node: Inbound) {
    void operation.run(() => api<Inbound>(`/inbounds/${node.id}/toggle`, { method: 'POST', body: { enabled: !node.enabled } }), async () => { setAction(null); await refreshResources(client) })
  }
  return <Page title={t.pages.inbounds} description={t.inbounds.description} actions={<Button asChild><Link to={canCreateOnSelected ? `/inbounds/new?server=${selected.id}` : '/inbounds/new'}>{t.pages.newInbound}</Link></Button>}>
    {serverList.length > 1 && <label className="flex flex-wrap items-center gap-3 text-sm font-medium">
      {t.inbounds.server}
      <select className="h-9 w-full rounded-md border bg-background px-3 text-sm sm:w-auto sm:max-w-sm" value={selected?.id ?? ''} onChange={(event) => {
        const next = new URLSearchParams(params)
        if (event.target.value) next.set('server', event.target.value)
        else next.delete('server')
        setParams(next)
      }}>
        <option value="">{t.inbounds.allServers}</option>
        {serverList.map((server) => <option key={server.id} value={server.id}>{server.name}</option>)}
      </select>
    </label>}
    {operation.error != null && !action && <ErrorNotice error={operation.error} />}
    {servers.isError && <ErrorNotice error={servers.error} retry={() => { void servers.refetch() }} />}
    {query.isPending || (params.has('server') && servers.isPending) ? <Loading /> : query.isError ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} /> : visibleNodes.length === 0 ? <Empty>{selected ? t.inbounds.serverEmpty : t.inbounds.empty}</Empty> : <div className="grid min-w-0 gap-4 lg:grid-cols-2">
      {visibleNodes.map((node) => {
        const server = serverList.find(({ id }) => id === node.server_id)
        return <Panel key={node.id} title={node.remark || node.tag} hint={node.tag}>
          <div className="flex flex-wrap gap-2"><Badge variant="secondary">{node.protocol === 'vless_reality' ? t.inbounds.reality : t.inbounds.hysteria2}</Badge><Badge variant={node.enabled ? 'default' : 'outline'}>{node.enabled ? t.common.enabled : t.common.disabled}</Badge></div>
          <p className="text-sm">{t.inbounds.port}: {node.port} / {node.protocol === 'vless_reality' ? 'TCP' : 'UDP'}</p>
          {server && <p className="text-sm text-muted-foreground">{t.servers.owner(server.name)}</p>}
          {node.key_status === 'pending' && <p className="text-sm text-muted-foreground">{t.inbounds.pendingKeys}</p>}
          {!node.enabled && <p className="text-sm text-muted-foreground">{node.key_status ? t.inbounds.remoteApply : t.common.applyHint}</p>}
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" asChild><Link to={`/inbounds/${node.id}`}>{t.common.edit}</Link></Button>
              <Button variant="outline" disabled={operation.pending || (!!node.key_status && !server)} onClick={() => { operation.reset(); if (node.enabled) setAction({ node, kind: 'disable' }); else toggle(node) }}>{operation.pending ? t.common.working : node.enabled ? t.common.disable : t.common.enable}</Button>
              <Button variant="destructive" disabled={operation.pending} onClick={() => { operation.reset(); setAction({ node, kind: 'delete' }) }}>{t.common.delete}</Button>
            </div>
        </Panel>
      })}
    </div>}
    <Confirm open={!!action} onOpenChange={(open) => { if (!open) setAction(null) }} title={action?.kind === 'delete' ? t.inbounds.deleteTitle : t.common.disable} description={<>{action?.node.remark || action?.node.tag}<br />{action?.kind === 'delete' ? t.inbounds.deleteHint : t.common.disableHint}{action?.node.enabled && <><br />{action.node.key_status ? t.inbounds.remoteApply : t.common.applyHint}</>}</>} pending={operation.pending} error={operation.error} onConfirm={() => {
      if (!action) return
      if (action.kind === 'disable') toggle(action.node)
      else void operation.run(() => api<void>(`/inbounds/${action.node.id}`, { method: 'DELETE' }), async () => { setAction(null); await refreshResources(client) })
    }} />
  </Page>
}
