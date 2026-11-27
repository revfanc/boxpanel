import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useOperation } from '@/hooks/use-operation'
import { Confirm, Notice } from '@/components/ResourceUI'
import { ErrorNotice } from '@/components/Feedback'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { zhCN as t } from '@/i18n/zh-CN'

type UpgradeStatus = {
  state: 'idle' | 'unsupported' | 'running' | 'current' | 'succeeded' | 'failed'
  target?: string
  current?: string
}
const path = '/system/upgrade'
const queryKey = ['system', 'upgrade'] as const

export function MasterUpgrade() {
  const [open, setOpen] = useState(false)
  const client = useQueryClient()
  const operation = useOperation()
  const query = useQuery({
    queryKey, queryFn: ({ signal }) => api<UpgradeStatus>(path, { signal }),
    refetchInterval: (query) => query.state.data?.state === 'running' ? 3000 : false,
  })
  const status = query.data
  if (!status) return query.isError && <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
  if (status.state === 'unsupported') return <Notice>{t.servers.upgradeUnsupported}</Notice>
  const target = status.target ?? ''
  const current = status.current ?? ''
  return <>
    <Confirm open={open} onOpenChange={(value) => { operation.reset(); setOpen(value) }}
      title={t.servers.localUpgrade} description={t.servers.localUpgradeHint} pending={operation.pending} error={operation.error}
      trigger={<Button variant="outline" disabled={status.state === 'running'}>{t.servers.localUpgrade}</Button>}
      onConfirm={() => { void operation.run(() => api<UpgradeStatus>(path, { method: 'POST' }), async (next) => {
        await client.cancelQueries({ queryKey })
        client.setQueryData(queryKey, next)
        setOpen(false)
      }) }} />
    {status.state === 'running' && <Notice>{t.servers.upgradeRunning(target)}</Notice>}
    {status.state === 'failed' && <Notice>{t.servers.upgradeFailed(target, current)}</Notice>}
    {status.state === 'current' && <Notice>{t.servers.upgradeCurrent(current)}</Notice>}
    {status.state === 'succeeded' && <Notice>{t.servers.upgradeSucceeded(current)}</Notice>}
  </>
}
