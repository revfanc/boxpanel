import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Empty, Notice, Panel } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { APIError, api } from '@/lib/api'
import { unsupported } from '@/lib/resources'
import { zhCN as t } from '@/i18n/zh-CN'
import type { Server, ServerLogs } from '@/types/api'

type Viewer = {
  path: `/servers/${number}/logs/${string}`
  timer?: number
  request?: Promise<void>
}

function useLogLease(id: number, enabled: boolean) {
  const [error, setError] = useState<unknown>(null)
  useEffect(() => {
    if (!enabled) return
    let viewer: Viewer | undefined
    function close(keepalive: boolean) {
      const previous = viewer
      if (!previous) return
      viewer = undefined
      window.clearTimeout(previous.timer)
      const release = () => api<void>(previous.path, { method: 'DELETE', keepalive: true }).catch(() => {})
      // A regular tab switch waits for an in-flight renewal to finish so
      // the DELETE cannot overtake it. Page teardown also sends immediately.
      if (keepalive || !previous.request) void release()
      if (previous.request) void previous.request.then(release)
    }
    function open() {
      if (viewer || document.hidden) return
      const token = Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, '0')).join('')
      const current: Viewer = { path: `/servers/${id}/logs/${token}` }
      viewer = current
      function renew() {
        current.request = api<void>(current.path, { method: 'PUT', signal: AbortSignal.timeout(5000) })
          .then(() => { if (viewer === current) setError(null) })
          .catch((error: unknown) => { if (viewer === current) setError(error) })
          .finally(() => {
            current.request = undefined
            if (viewer === current) current.timer = window.setTimeout(renew, 15_000)
          })
      }
      renew()
    }
    const visibility = () => { if (document.hidden) close(false); else open() }
    const hide = () => close(true)
    open()
    window.addEventListener('pagehide', hide)
    window.addEventListener('pageshow', open)
    document.addEventListener('visibilitychange', visibility)
    return () => {
      window.removeEventListener('pagehide', hide)
      window.removeEventListener('pageshow', open)
      document.removeEventListener('visibilitychange', visibility)
      close(false)
    }
  }, [enabled, id])
  return error
}

const upgradeRequired = (error: unknown) => error instanceof APIError && error.code === 'node_monitoring_required'

function LogLines({ lines }: { lines: string[] }) {
  const view = useRef<HTMLPreElement>(null)
  const follow = useRef(true)
  const text = lines.join('\n')
  useLayoutEffect(() => {
    if (view.current && follow.current) view.current.scrollTop = view.current.scrollHeight
  }, [text])
  return <pre ref={view} tabIndex={0} onScroll={({ currentTarget: el }) => {
    follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }} className="max-h-[65svh] min-w-0 overflow-y-auto rounded-xl border bg-muted/30 p-4 font-mono text-xs leading-6 whitespace-pre-wrap break-all"
    aria-label={t.pages.logs}>{text}</pre>
}

export function ServerLogsTab({ server }: { server: Server }) {
  const waitingForJoin = !server.is_local && server.last_seen_at === null
  const leaseError = useLogLease(server.id, !server.is_local && !waitingForJoin)
  const logs = useQuery({
    queryKey: ['servers', server.id, 'logs'],
    queryFn: ({ signal }) => api<ServerLogs>(`/servers/${server.id}/logs`, { signal }),
    enabled: !waitingForJoin,
    staleTime: 0,
    gcTime: 0,
    refetchInterval: (query) => unsupported(query.state.error) ? false : server.is_local ? 5000 : 3000,
  })
  const error = logs.error ?? leaseError
  return <Panel title={t.pages.logs} hint={t.logs.description}>
    {waitingForJoin || server.status === 'offline' ? <Notice>{t.logs.waitingOnline}</Notice> : null}
    {error != null && (upgradeRequired(error) ? <Notice>{t.servers.metricsUpgrade} · {t.servers.nodeOutdated}</Notice>
      : unsupported(error) ? <Notice>{t.logs.unavailable}</Notice>
        : <ErrorNotice error={error} retry={() => { void logs.refetch() }} />)}
    {!waitingForJoin && !error && (server.is_local && logs.isPending ? <Loading /> : !logs.data?.ready && <>
      <Notice>{t.logs.requesting}</Notice>
      <p className="text-sm text-muted-foreground">{t.logs.waitingHint}</p>
    </>)}
    {logs.data?.ready && (logs.data.lines.length
      ? <LogLines lines={logs.data.lines} />
      : <Empty>{t.logs.empty}</Empty>)}
    {!waitingForJoin && <Button variant="outline" disabled={logs.isFetching} onClick={() => { void logs.refetch() }}>{t.common.refresh}</Button>}
  </Panel>
}
