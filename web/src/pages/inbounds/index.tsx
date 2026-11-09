import { useState } from 'react'
import { Link } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Page, Panel, Empty, Confirm } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { inboundsQuery, refreshResources } from '@/lib/resources'
import { api } from '@/lib/api'
import { useOperation } from '@/hooks/use-operation'
import type { Inbound } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export function InboundsPage() {
  const query = useQuery(inboundsQuery)
  const client = useQueryClient()
  const operation = useOperation()
  const [action, setAction] = useState<{ node: Inbound; kind: 'delete' | 'disable' } | null>(null)
  function toggle(node: Inbound) {
    void operation.run(() => api<Inbound>(`/inbounds/${node.id}/toggle`, { method: 'POST', body: { enabled: !node.enabled } }), async () => { setAction(null); await refreshResources(client) })
  }
  return <Page title={t.pages.inbounds} description={t.inbounds.description} actions={<Button asChild><Link to="/inbounds/new">{t.pages.newInbound}</Link></Button>}>
    {operation.error != null && !action && <ErrorNotice error={operation.error} />}
    {query.isPending ? <Loading /> : query.isError ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} /> : query.data.length === 0 ? <Empty>{t.inbounds.empty}</Empty> : <div className="grid min-w-0 gap-4 lg:grid-cols-2">
      {query.data.map((node) => <Panel key={node.id} title={node.remark || node.tag} hint={node.tag}>
        <div className="flex flex-wrap gap-2"><Badge variant="secondary">{node.protocol === 'vless_reality' ? t.inbounds.reality : t.inbounds.hysteria2}</Badge><Badge variant={node.enabled ? 'default' : 'outline'}>{node.enabled ? t.common.enabled : t.common.disabled}</Badge></div>
        <p className="text-sm">{t.inbounds.port}: {node.port} / {node.protocol === 'vless_reality' ? 'TCP' : 'UDP'}</p>
        <div className="flex flex-wrap gap-2"><Button variant="outline" asChild><Link to={`/inbounds/${node.id}`}>{t.common.edit}</Link></Button>
          <Button variant="outline" disabled={operation.pending} onClick={() => { operation.reset(); if (node.enabled) setAction({ node, kind: 'disable' }); else toggle(node) }}>{operation.pending ? t.common.working : node.enabled ? t.common.disable : t.common.enable}</Button>
          <Button variant="destructive" disabled={operation.pending} onClick={() => { operation.reset(); setAction({ node, kind: 'delete' }) }}>{t.common.delete}</Button></div>
      </Panel>)}
    </div>}
    <Confirm open={!!action} onOpenChange={(open) => { if (!open) setAction(null) }} title={action?.kind === 'delete' ? t.inbounds.deleteTitle : t.common.disable} description={<>{action?.node.remark || action?.node.tag}<br />{action?.kind === 'delete' ? t.inbounds.deleteHint : t.common.disableHint}</>} pending={operation.pending} error={operation.error} onConfirm={() => {
      if (!action) return
      if (action.kind === 'disable') toggle(action.node)
      else void operation.run(() => api<void>(`/inbounds/${action.node.id}`, { method: 'DELETE' }), async () => { setAction(null); await refreshResources(client) })
    }} />
  </Page>
}
