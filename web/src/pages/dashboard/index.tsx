import { useState } from 'react'
import { Link } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Page, Panel, Empty, Notice, Confirm } from '@/components/ResourceUI'
import { systemQuery } from '@/lib/query'
import { inboundsQuery, usersQuery, refreshResources, unsupported } from '@/lib/resources'
import { api } from '@/lib/api'
import { bytes, duration } from '@/lib/format'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'

export function DashboardPage() {
  const system = useQuery({ ...systemQuery, refetchInterval: 5000 })
  const inbounds = useQuery({ ...inboundsQuery, refetchInterval: 5000 })
  const users = useQuery({ ...usersQuery, refetchInterval: 5000 })
  const client = useQueryClient()
  const restart = useOperation()
  const [confirm, setConfirm] = useState(false)
  const current = system.data
  return <Page title={t.pages.home} description={t.dashboard.description}>
    {system.isPending && <Loading />}
    {system.isError && (unsupported(system.error) ? <Notice>{t.common.devHint}</Notice> : <ErrorNotice error={system.error} retry={() => { void system.refetch() }} />)}
    {current && !system.isError && (!current.supported ? <Notice>{t.common.devHint}</Notice> : <>
      <div className="grid min-w-0 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Panel title={t.dashboard.cpu}><p className="text-2xl font-semibold tabular-nums">{current.cpu.toFixed(1)}%</p></Panel>
        <Panel title={t.dashboard.memory}><p>{bytes(current.mem.used)} / {bytes(current.mem.total)}</p></Panel>
        <Panel title={t.dashboard.disk}><p>{bytes(current.disk.used)} / {bytes(current.disk.total)}</p></Panel>
        <Panel title={t.dashboard.network}><p>↓ {bytes(current.net.rx_rate)}/s</p><p>↑ {bytes(current.net.tx_rate)}/s</p></Panel>
      </div>
      <Panel title={t.status.label}>
        <div className="flex flex-wrap items-center gap-3"><span className="font-medium">{current.singbox.running ? t.status.running : current.singbox.installed ? t.status.stopped : t.status.missing}</span><span className="text-muted-foreground">{current.singbox.version}</span></div>
        <p className="text-sm text-muted-foreground">{t.dashboard.uptime}: {duration(current.uptime)} · {t.dashboard.kernelUptime}: {duration(current.singbox.uptime)}</p>
        <Confirm open={confirm} onOpenChange={(value) => { restart.reset(); setConfirm(value) }} title={t.dashboard.restart} description={t.dashboard.restartHint} pending={restart.pending} error={restart.error} trigger={<Button variant="outline" disabled={!current.singbox.installed}>{t.dashboard.restart}</Button>} onConfirm={() => { void restart.run(() => api<void>('/singbox/restart', { method: 'POST' }), async () => { setConfirm(false); await refreshResources(client) }) }} />
        {restart.success && <Notice>{t.dashboard.restarted}</Notice>}
      </Panel>
    </>)}
    <div className="grid min-w-0 gap-4 sm:grid-cols-2">
      <Panel title={t.dashboard.nodes}>{inbounds.isPending ? <Loading /> : inbounds.isError ? <ErrorNotice error={inbounds.error} retry={() => { void inbounds.refetch() }} /> : <p className="text-3xl font-semibold tabular-nums">{inbounds.data.length}</p>}</Panel>
      <Panel title={t.dashboard.users}>{users.isPending ? <Loading /> : users.isError ? <ErrorNotice error={users.error} retry={() => { void users.refetch() }} /> : <p className="text-3xl font-semibold tabular-nums">{users.data.length}</p>}</Panel>
    </div>
    {(inbounds.data?.length === 0 || users.data?.length === 0) && <Empty>{t.dashboard.empty}</Empty>}
    <Panel title={t.dashboard.shortcuts}><div className="flex flex-wrap gap-3"><Button asChild><Link to="/inbounds/new">{t.pages.newInbound}</Link></Button><Button asChild variant="outline"><Link to="/users">{t.pages.users}</Link></Button><Button asChild variant="outline"><Link to="/rules">{t.pages.rules}</Link></Button></div></Panel>
  </Page>
}
