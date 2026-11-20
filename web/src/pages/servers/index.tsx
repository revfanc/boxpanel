import { useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { CopyField } from '@/components/CopyField'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Confirm, Empty, Field, Page, Panel, Submit } from '@/components/ResourceUI'
import { Button } from '@/components/ui/button'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'
import { hostSchema } from '@/schemas/settings'
import { api } from '@/lib/api'
import { bytes } from '@/lib/format'
import { refreshResources, serversQuery } from '@/lib/resources'
import type { IssuedJoinToken, JoinToken, Server } from '@/types/api'

const renameSchema = z.object({
  name: z.string().refine((name) => {
    const length = Array.from(name.trim()).length
    return length >= 1 && length <= 64 && !/\p{Cc}/u.test(name)
  }, t.servers.invalidName).transform((name) => name.trim()),
})

const serverSchema = renameSchema.extend({ public_host: hostSchema.optional() })

function EditServerForm({ server, close }: { server: Server; close: () => void }) {
  const client = useQueryClient()
  const operation = useOperation()
  const form = useForm<z.infer<typeof serverSchema>>({
    resolver: zodResolver(serverSchema),
    defaultValues: server.is_local ? { name: server.name } : { name: server.name, public_host: server.public_host },
  })
  return <form className="space-y-3" onSubmit={form.handleSubmit((body) => operation.run(
    () => api<Server>(`/servers/${server.id}`, { method: 'PUT', body }),
    async (updated) => {
      await client.cancelQueries({ queryKey: serversQuery.queryKey })
      client.setQueryData(serversQuery.queryKey, (current) => current?.map((row) => row.id === updated.id
        ? { ...updated, traffic_up: row.traffic_up, traffic_down: row.traffic_down } : row))
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

const tokenKey = ['servers', 'join-tokens'] as const
const syncErrors: Readonly<Record<string, string>> = t.servers.syncErrors

function joinCommand(issued: IssuedJoinToken) {
  const version = window.__BOXPANEL__?.version ?? ''
  const tag = /^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9]+(?:\.[A-Za-z0-9]+)*)?$/.test(version) ? version : ''
  const script = `https://raw.githubusercontent.com/revfanc/boxpanel/${tag || 'main'}/scripts/install.sh`
  const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'"
  return `bash <(curl -fsSL ${script}) --join ${quote(issued.master_url)} --token ${quote(issued.token)}`
    + ` --master-pin ${quote(issued.master_pin)}`
    + (tag ? ` --version ${tag}` : '')
}

function JoinForm() {
  const client = useQueryClient()
  const operation = useOperation()
  const revoke = useOperation()
  const tokens = useQuery({
    queryKey: tokenKey,
    queryFn: ({ signal }) => api<JoinToken[]>('/servers/join-tokens', { signal }),
    refetchInterval: 30_000,
  })
  const [issued, setIssued] = useState<IssuedJoinToken | null>(null)
  const form = useForm<z.infer<typeof renameSchema>>({ resolver: zodResolver(renameSchema), defaultValues: { name: '' } })
  const active = tokens.data ?? []
  return <Panel title={t.servers.add} hint={t.servers.addHint}>
    <form className="space-y-3" onSubmit={form.handleSubmit((body) => operation.run(
      () => api<IssuedJoinToken>('/servers/join-tokens', { method: 'POST', body }),
      async (result) => {
        const { id, name, expires_at } = result
        await client.cancelQueries({ queryKey: tokenKey })
        client.setQueryData<JoinToken[]>(tokenKey, (current) => [{ id, name, expires_at }, ...(current ?? [])])
        setIssued(result)
        form.reset()
        await client.invalidateQueries({ queryKey: tokenKey })
      },
    ))}>
      <Field label={t.servers.name} error={form.formState.errors.name?.message} disabled={operation.pending} {...form.register('name')} />
      {operation.error != null && <ErrorNotice error={operation.error} />}
      <Submit pending={operation.pending} label={t.servers.generate} />
    </form>
    {issued && active.some((token) => token.id === issued.id) &&
      <CopyField key={issued.id} label={t.servers.command} value={joinCommand(issued)} />}
    <div className="space-y-2 border-t pt-4">
      <h2 className="text-sm font-medium">{t.servers.activeTokens}</h2>
      {tokens.isPending ? <Loading /> : tokens.isError ? <ErrorNotice error={tokens.error} retry={() => { void tokens.refetch() }} />
        : active.length === 0 ? <p className="text-sm text-muted-foreground">{t.servers.noTokens}</p>
          : <ul className="divide-y">
            {active.map((token) => <li key={token.id} className="flex flex-wrap items-center justify-between gap-3 py-2">
              <div className="min-w-0">
                <p className="text-sm font-medium">{token.name}</p>
                <p className="text-xs text-muted-foreground">{t.servers.expires} {new Date(token.expires_at * 1000).toLocaleString('zh-CN')}</p>
              </div>
              <Button type="button" size="sm" variant="outline" disabled={revoke.pending} aria-label={`${t.servers.revoke} ${token.name}`} onClick={() => {
                void revoke.run(() => api<void>(`/servers/join-tokens/${token.id}`, { method: 'DELETE' }), async () => {
                  if (issued?.id === token.id) setIssued(null)
                  await client.cancelQueries({ queryKey: tokenKey })
                  client.setQueryData<JoinToken[]>(tokenKey, (current) => current?.filter((row) => row.id !== token.id))
                  await client.invalidateQueries({ queryKey: tokenKey })
                })
              }}>{t.servers.revoke}</Button>
            </li>)}
          </ul>}
      {revoke.error != null && <ErrorNotice error={revoke.error} />}
    </div>
  </Panel>
}

export function ServersPage() {
  const client = useQueryClient()
  const query = useQuery({ ...serversQuery, refetchInterval: 30_000 })
  const operation = useOperation()
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<number | null>(null)
  const [removing, setRemoving] = useState<Server | null>(null)
  return <Page title={t.pages.servers} actions={<Button aria-expanded={adding} onClick={() => setAdding(!adding)}>{adding ? t.servers.closeAdd : t.servers.add}</Button>}>
    {adding && <JoinForm />}
    {query.isPending ? <Loading /> : query.isError ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
      : query.data.length === 0 ? <Empty>{t.servers.empty}</Empty> : <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        {query.data.map((server) => <Panel key={server.id} title={server.name}>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted-foreground">{t.servers.address}</dt><dd>{server.public_host || t.common.unknown}</dd>
            <dt className="text-muted-foreground">{t.servers.status}</dt><dd>{t.servers.statuses[server.status]}</dd>
            <dt className="text-muted-foreground">{t.servers.version}</dt><dd>{server.version || t.common.unknown}</dd>
            {!server.is_local && <>
              {server.sync_status && <>
                <dt className="text-muted-foreground">{t.servers.sync}</dt><dd>{t.servers.syncStatuses[server.sync_status]}</dd>
                <dt className="text-muted-foreground">{t.servers.generations}</dt><dd>{server.applied_generation} / {server.desired_generation}</dd>
              </>}
              {server.singbox_active !== undefined && <>
                <dt className="text-muted-foreground">{t.servers.core}</dt><dd>{server.singbox_active ? t.servers.coreActive : t.servers.coreInactive}</dd>
              </>}
              <dt className="text-muted-foreground">{t.servers.lastSeen}</dt><dd>{server.last_seen_at === null ? t.servers.noHeartbeat : new Date(server.last_seen_at * 1000).toLocaleString('zh-CN')}</dd>
              {server.traffic_up !== undefined && server.traffic_down !== undefined && <>
                <dt className="text-muted-foreground">{t.servers.traffic}</dt><dd>{bytes(server.traffic_up)} / {bytes(server.traffic_down)}</dd>
              </>}
            </>}
          </dl>
          {server.sync_error && <p role="status" className="text-sm text-destructive">{syncErrors[server.sync_error] ?? t.servers.syncErrors.apply_failed}</p>}
          {!server.is_local && <p className="text-xs text-muted-foreground">{t.servers.keyRefresh}</p>}
          {editing === server.id ? <EditServerForm server={server} close={() => setEditing(null)} />
            : <div className="flex flex-wrap gap-2">
              <Button variant="outline" disabled={editing !== null} onClick={() => setEditing(server.id)}>{server.is_local ? t.servers.rename : t.common.edit}</Button>
              {!server.is_local && <Button variant="outline" disabled={editing !== null} onClick={() => { operation.reset(); setRemoving(server) }}>{t.servers.remove}</Button>}
            </div>}
        </Panel>)}
      </div>}
    {removing && <Confirm open onOpenChange={(open) => { if (!open) setRemoving(null) }}
      title={t.servers.remove} description={t.servers.removeHint(removing.name)} pending={operation.pending} error={operation.error} onConfirm={() => {
        void operation.run(() => api<void>(`/servers/${removing.id}`, { method: 'DELETE' }), async () => {
          await refreshResources(client)
          setRemoving(null)
        })
      }} />}
  </Page>
}
