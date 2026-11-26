import { useEffect, useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { Link } from 'react-router'
import { CopyField } from '@/components/CopyField'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Field, Notice, Panel, Submit } from '@/components/ResourceUI'
import { Button } from '@/components/ui/button'
import { LinkButton } from '@/components/LinkButton'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'
import { api, APIError } from '@/lib/api'
import { expiry } from '@/lib/format'
import { serversQuery } from '@/lib/resources'
import { serverNameSchema } from '@/schemas/server'
import type { IssuedJoinToken, JoinToken, Server } from '@/types/api'
import { installCommand } from './install-command'

const tokenPath = '/servers/join-tokens'
const tokenKey = ['servers', 'join-tokens'] as const

function joinCommand(issued: IssuedJoinToken) {
  const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'"
  const args = `--join ${quote(issued.master_url)} --token ${quote(issued.token)} --master-pin ${quote(issued.master_pin)}`
  return installCommand(window.__BOXPANEL__?.version ?? '', args)
}

type JoinProgress = { state: 'waiting' | 'unavailable' } | { state: 'connected'; server: Server }

function WaitForNode({ tokenID, regenerate }: { tokenID: number; regenerate: () => void }) {
  const client = useQueryClient()
  const query = useQuery({
    queryKey: [...tokenKey, tokenID],
    queryFn: async ({ signal }): Promise<JoinProgress> => {
      const [servers, token] = await Promise.all([
        api<Server[]>('/servers', { signal }),
        api<JoinToken>(`${tokenPath}/${tokenID}`, { signal }).catch((error: unknown) => {
          if (error instanceof APIError && error.status === 404) return null
          throw error
        }),
      ])
      if (!token) return { state: 'unavailable' }
      const server = servers.find((row) => row.id === token.server_id && !row.is_local && row.last_seen_at !== null)
      return server ? { state: 'connected', server } : { state: 'waiting' }
    },
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    refetchInterval: ({ state }) => !state.data || state.data.state === 'waiting' ? 3000 : false,
  })
  const state = query.data?.state
  useEffect(() => {
    if (state === 'connected') void client.invalidateQueries({ queryKey: serversQuery.queryKey, exact: true })
    if (state === 'connected' || state === 'unavailable') void client.invalidateQueries({ queryKey: tokenKey, exact: true })
  }, [client, state])

  if (query.data?.state === 'connected') {
    const { id, name, public_host, version } = query.data.server
    return <>
      <Notice>{t.servers.connected(name, public_host, version || t.common.unknown)}</Notice>
      <LinkButton render={<Link to={`/inbounds/new?server=${id}`} />}>{t.servers.createInbound}</LinkButton>
    </>
  }
  if (state === 'unavailable') return <>
    <Notice>{t.servers.joinUnavailable}</Notice>
    <Button onClick={regenerate}>{t.servers.regenerate}</Button>
  </>
  return <>
    <Notice>{t.servers.waiting}</Notice>
    {query.isError && <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />}
  </>
}

export function JoinWizard() {
  const client = useQueryClient()
  const issue = useOperation()
  const revoke = useOperation()
  const tokens = useQuery({
    queryKey: tokenKey,
    queryFn: ({ signal }) => api<JoinToken[]>(tokenPath, { signal }),
    refetchInterval: 30_000,
  })
  const [issued, setIssued] = useState<IssuedJoinToken | null>(null)
  const [waiting, setWaiting] = useState(false)
  const form = useForm({ resolver: zodResolver(serverNameSchema), defaultValues: { name: '' } })
  const active = tokens.data ?? []
  const waitingForNode = waiting || (issued !== null && tokens.data !== undefined && !active.some((token) => token.id === issued.id))
  const stage = !issued ? 0 : waitingForNode ? 2 : 1

  return <Panel title={t.servers.add}>
    <ol className="grid gap-2 text-sm sm:grid-cols-3">
      {t.servers.joinSteps.map((step, index) => <li key={step} aria-current={index === stage ? 'step' : undefined}
        className={index === stage ? 'font-semibold' : 'text-muted-foreground'}>{index + 1}. {step}</li>)}
    </ol>
    {!issued ? <form className="space-y-3" onSubmit={form.handleSubmit((body) => issue.run(
      () => api<IssuedJoinToken>(tokenPath, { method: 'POST', body }),
      async (result) => {
        const { id, name, expires_at } = result
        await client.cancelQueries({ queryKey: tokenKey, exact: true })
        client.setQueryData<JoinToken[]>(tokenKey, (current) => [{ id, name, expires_at }, ...(current ?? [])])
        setIssued(result)
        await client.invalidateQueries({ queryKey: tokenKey, exact: true })
      },
    ))}>
      <Field label={t.servers.name} error={form.formState.errors.name?.message} disabled={issue.pending} {...form.register('name')} />
      {issue.error != null && <ErrorNotice error={issue.error} />}
      <Submit pending={issue.pending} label={t.servers.generate} />
    </form> : waitingForNode ? <WaitForNode tokenID={issued.id} regenerate={() => { setWaiting(false); setIssued(null) }} /> : <>
      <CopyField key={issued.id} label={t.servers.command} value={joinCommand(issued)} />
      <ul className="list-disc space-y-1 pl-5 text-sm leading-6 text-muted-foreground">
        {[t.servers.joinRoot, t.servers.joinPorts, t.servers.joinNAT, t.servers.joinLifetime].map((hint) => <li key={hint}>{hint}</li>)}
      </ul>
      <Button onClick={() => setWaiting(true)}>{t.servers.waitForNode}</Button>
    </>}
    <div className="space-y-2 border-t pt-4">
      <h2 className="text-sm font-medium">{t.servers.activeTokens}</h2>
      {tokens.isPending ? <Loading /> : tokens.isError ? <ErrorNotice error={tokens.error} retry={() => { void tokens.refetch() }} />
        : active.length === 0 ? <p className="text-sm text-muted-foreground">{t.servers.noTokens}</p>
          : <ul className="divide-y">
            {active.map((token) => <li key={token.id} className="flex flex-wrap items-center justify-between gap-3 py-2">
              <div className="min-w-0">
                <p className="text-sm font-medium">{token.name}</p>
                <p className="text-xs text-muted-foreground">{t.servers.expires} {expiry(token.expires_at)}</p>
              </div>
              <Button type="button" size="sm" variant="outline" disabled={revoke.pending} aria-label={`${t.servers.revoke} ${token.name}`} onClick={() => {
                void revoke.run(() => api<void>(`${tokenPath}/${token.id}`, { method: 'DELETE' }), async () => {
                  if (issued?.id === token.id) setWaiting(true)
                  await client.cancelQueries({ queryKey: tokenKey, exact: true })
                  client.setQueryData<JoinToken[]>(tokenKey, (current) => current?.filter((row) => row.id !== token.id))
                  await client.invalidateQueries({ queryKey: tokenKey, exact: true })
                  await client.invalidateQueries({ queryKey: [...tokenKey, token.id], exact: true })
                })
              }}>{t.servers.revoke}</Button>
            </li>)}
          </ul>}
      {revoke.error != null && <ErrorNotice error={revoke.error} />}
    </div>
  </Panel>
}
