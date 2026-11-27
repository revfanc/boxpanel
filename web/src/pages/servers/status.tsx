import { useId, useState } from 'react'
import { useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Controller, useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { Confirm, Field, Notice, Panel, Submit } from '@/components/ResourceUI'
import { CopyField } from '@/components/CopyField'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { useOperation } from '@/hooks/use-operation'
import { api } from '@/lib/api'
import { duration, expiry, metricRates, metricUsage, relativeTime } from '@/lib/format'
import { refreshResources, serverQuery, serversQuery, serverStatusQuery, unsupported } from '@/lib/resources'
import { queryKeys } from '@/lib/query'
import { serverSchema } from '@/schemas/server'
import type { RestartState, Server, ServerStatus } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'
import { MasterUpgrade } from './master-upgrade'
import { installCommand } from './install-command'
import { ServerBadges } from './status-badges'

function EditServerForm({ server, close }: { server: Server; close: () => void }) {
  const client = useQueryClient()
  const operation = useOperation()
  const form = useForm({
    resolver: zodResolver(serverSchema),
    defaultValues: server.is_local ? { name: server.name } : { name: server.name, public_host: server.public_host, auto_upgrade: server.auto_upgrade ?? false },
  })
  const autoID = useId()
  return <form className="space-y-3" onSubmit={form.handleSubmit((body) => operation.run(
    () => api<Server>(`/servers/${server.id}`, { method: 'PUT', body }),
    async (updated) => {
      const detailKey = serverQuery(server.id).queryKey
      await client.cancelQueries({ queryKey: queryKeys.servers })
      client.setQueryData(detailKey, (current) => current && { ...current, ...updated })
      close()
      await refreshResources(client)
    },
  ))}>
    <Field label={t.servers.name} hint={t.servers.renameHint} error={form.formState.errors.name?.message} disabled={operation.pending} {...form.register('name')} />
    {!server.is_local && <>
      <Field label={t.servers.address} hint={t.exitServers.serverAddressHint} error={form.formState.errors.public_host?.message} disabled={operation.pending} {...form.register('public_host')} />
      <div className="space-y-1">
        <Label htmlFor={autoID} className="cursor-pointer gap-3 font-normal">
          <Controller name="auto_upgrade" control={form.control} render={({ field }) => (
            <Checkbox id={autoID} name={field.name} checked={field.value ?? false} disabled={operation.pending}
              onCheckedChange={(checked) => field.onChange(checked)} onBlur={field.onBlur} ref={field.ref} />
          )} />
          <span>{t.servers.autoUpgrade}</span>
        </Label>
        <p className="text-sm text-muted-foreground">{t.servers.autoUpgradeHint}</p>
      </div>
    </>}
    {operation.error != null && <ErrorNotice error={operation.error} />}
    <div className="flex flex-wrap gap-2">
      <Submit pending={operation.pending} />
      <Button type="button" variant="outline" disabled={operation.pending} onClick={close}>{t.common.cancel}</Button>
    </div>
  </form>
}

const syncErrors: Readonly<Record<string, string>> = t.servers.syncErrors
function SystemMetrics({ server, status }: { server: Server; status: ServerStatus }) {
  const metrics = status.metrics
  if (!metrics?.supported) {
    return <Notice>{!server.is_local && !metrics
      ? (server.last_seen_at === null ? t.servers.noHeartbeat : t.servers.metricsUpgrade)
      : t.servers.metricsUnavailable}</Notice>
  }
  const fields = [
    [t.dashboard.cpu, `${metrics.cpu.toFixed(1)}%`],
    [t.dashboard.memory, metricUsage(metrics.mem)],
    [t.dashboard.disk, metricUsage(metrics.disk)],
    [t.dashboard.network, metricRates(metrics.net)],
  ]
  return <div className="grid min-w-0 grid-cols-2 gap-3 xl:grid-cols-4">
    {fields.map(([label, value]) => <div key={label} className="min-w-0 rounded-xl border bg-card p-4">
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="mt-1 text-base font-semibold tabular-nums">{value}</p>
    </div>)}
  </div>
}

function ServerKernel({ server, status }: { server: Server; status: ServerStatus }) {
  const client = useQueryClient()
  const operation = useOperation()
  const [confirm, setConfirm] = useState(false)
  const restart = status.restart
  const core = status.singbox
  const coreLabel = status.singbox_active === true ? t.status.running
    : core?.installed === false ? t.status.missing
      : status.singbox_active === false ? t.servers.coreInactive : t.status.unavailable
  const canRestart = server.is_local ? core?.installed : !!status.metrics && !server.upgrade_required
  return <Panel title={t.servers.core}>
    <div className="flex flex-wrap items-baseline gap-2 text-sm">
      <span className="font-medium">{coreLabel}</span>
      {core?.version && <span className="text-muted-foreground">{core.version}</span>}
    </div>
    {core && core.running && <p className="text-sm text-muted-foreground">{t.dashboard.kernelUptime} · {duration(core.uptime)}</p>}
    {status.metrics?.supported && <p className="text-sm text-muted-foreground">{t.dashboard.uptime} · {duration(status.metrics.uptime)}</p>}
    {canRestart && <Confirm open={confirm} onOpenChange={(open) => { operation.reset(); setConfirm(open) }}
      title={t.servers.restart} description={t.servers.restartHint} pending={operation.pending} error={operation.error}
      trigger={<Button variant="outline" disabled={restart?.status === 'pending'}>{t.servers.restart}</Button>}
      onConfirm={() => { void operation.run(() => api<RestartState | void>(`/servers/${server.id}/restart`, { method: 'POST' }), async (queued) => {
        const statusKey = serverStatusQuery(server.id).queryKey
        await client.cancelQueries({ queryKey: statusKey, exact: true })
        if (queued) client.setQueryData(statusKey, (current) => current && ({ ...current, restart: queued }))
        setConfirm(false)
        await Promise.all([
          client.invalidateQueries({ queryKey: statusKey, exact: true }),
          client.invalidateQueries({ queryKey: queryKeys.overview }),
        ])
      }) }} />}
    {restart?.status === 'pending' && <Notice>{t.servers.restartPending}</Notice>}
    {(restart?.status === 'succeeded' || server.is_local && operation.success) && <Notice>{t.servers.restarted}</Notice>}
    {restart?.status === 'failed' && <Notice>{restart.error === 'interrupted' ? t.servers.restartInterrupted
      : syncErrors[restart.error ?? ''] ?? t.servers.restartFailed}</Notice>}
  </Panel>
}

function ServerManagement({ server, status, now }: { server: Server; status?: ServerStatus; now: number }) {
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const operation = useOperation()
  const client = useQueryClient()
  const navigate = useNavigate()
  const servers = useQuery({ ...serversQuery, enabled: !server.is_local && !window.__BOXPANEL__?.version })
  const masterVersion = window.__BOXPANEL__?.version || servers.data?.find((row) => row.is_local)?.version
  const versionDiffers = !server.is_local && !!masterVersion && !!server.version && server.version !== masterVersion
  const oldMetrics = !server.is_local && server.last_seen_at !== null && status?.metrics === null
  const lastSeen = server.last_seen_at
  return <Panel title={t.servers.management}>
    <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm [&>dt]:text-muted-foreground">
      <dt>{t.servers.version}</dt><dd>{server.version || t.common.unknown}</dd>
      {server.inbound_counts && <><dt>{t.servers.inboundCounts}</dt><dd>{server.inbound_counts.ready} / {server.inbound_counts.pending}</dd></>}
      {!server.is_local && <>
        <dt>{t.servers.generations}</dt><dd>{server.applied_generation} / {server.desired_generation}</dd>
        <dt>{t.servers.lastSeen}</dt><dd>{lastSeen === null ? t.servers.noHeartbeat
          : <time dateTime={new Date(lastSeen * 1000).toISOString()} title={expiry(lastSeen)}>{relativeTime(lastSeen, now)}</time>}</dd>
        {server.uptime !== undefined && <><dt>{t.servers.uptime}</dt>
          <dd>{server.uptime < 60 ? t.servers.uptimeSeconds(server.uptime) : duration(server.uptime)}</dd></>}
        <dt>{t.servers.autoUpgrade}</dt><dd>{server.auto_upgrade ? t.servers.autoUpgradeOn : t.servers.autoUpgradeOff}</dd>
      </>}
    </dl>
    {versionDiffers && <p className="text-sm text-muted-foreground">{t.servers.versionDifference(masterVersion)}</p>}
    {versionDiffers && server.auto_upgrade && <Notice>{t.servers.autoUpgradePending}</Notice>}
    {server.upgrade_required && <Notice>{t.servers.nodeOutdated}</Notice>}
    {server.is_local ? <MasterUpgrade /> : (server.upgrade_required || versionDiffers || oldMetrics) && <CopyField
      label={t.servers.upgradeCommand} value={installCommand(masterVersion ?? '', 'update')} />}
    {server.sync_error && <Notice>{syncErrors[server.sync_error] ?? t.servers.syncErrors.apply_failed}</Notice>}
    {editing ? <EditServerForm server={server} close={() => setEditing(false)} /> : <div className="flex flex-wrap gap-2">
      <Button variant="outline" onClick={() => setEditing(true)}>{server.is_local ? t.servers.rename : t.common.edit}</Button>
      {!server.is_local && <Button variant="outline" onClick={() => { operation.reset(); setRemoving(true) }}>{t.servers.remove}</Button>}
    </div>}
    {!server.is_local && <Confirm open={removing} onOpenChange={setRemoving}
      title={t.servers.remove} description={t.servers.removeHint(server.name)} pending={operation.pending} error={operation.error}
      onConfirm={() => { void operation.run(() => api<void>(`/servers/${server.id}`, { method: 'DELETE' }), async () => {
        setRemoving(false)
        await navigate('/servers', { replace: true })
        await refreshResources(client)
      }) }} />}
  </Panel>
}

export function ServerStatusTab({ server }: { server: Server }) {
  const status = useQuery(serverStatusQuery(server.id))
  return <>
    <ServerBadges server={server} />
    {server.status === 'offline' && <Notice>{t.servers.metricsStale}</Notice>}
    {status.isPending && <Loading />}
    {status.isError && (unsupported(status.error) ? <Notice>{t.common.devHint}</Notice>
      : <ErrorNotice error={status.error} retry={() => { void status.refetch() }} />)}
    {status.data && <><SystemMetrics server={server} status={status.data} /><ServerKernel server={server} status={status.data} /></>}
    <ServerManagement server={server} status={status.data} now={status.dataUpdatedAt || server.updated_at * 1000} />
  </>
}
