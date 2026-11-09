import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Page, Toggle, Empty, Notice } from '@/components/ResourceUI'
import { Loading, ErrorNotice } from '@/components/Feedback'
import { api } from '@/lib/api'
import { queryKeys } from '@/lib/query'
import { unsupported } from '@/lib/resources'
import type { SingboxLogs } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export function LogsPage() {
  const [auto, setAuto] = useState(true)
  const logs = useQuery({ queryKey: queryKeys.logs, queryFn: ({ signal }) => api<SingboxLogs>('/singbox/logs?lines=200', { signal }), refetchInterval: (query) => auto && !unsupported(query.state.error) ? 5000 : false })
  return <Page title={t.pages.logs} description={t.logs.description} actions={<Button variant="outline" disabled={logs.isFetching} onClick={() => { void logs.refetch() }}>{logs.isFetching ? t.common.loading : t.common.refresh}</Button>}>
    <Toggle label={t.logs.auto} checked={auto} onChange={setAuto} />
    {logs.isPending && <Loading />}
    {logs.isError && (unsupported(logs.error) ? <Notice>{t.logs.unavailable}</Notice> : <ErrorNotice error={logs.error} retry={() => { void logs.refetch() }} />)}
    {logs.data && (logs.data.lines.length ? <pre className="max-h-[65svh] min-w-0 overflow-y-auto whitespace-pre-wrap break-all rounded-xl border bg-muted/30 p-4 font-mono text-xs leading-6" aria-label={t.pages.logs}>{logs.data.lines.join('\n')}</pre> : <Empty>{t.logs.empty}</Empty>)}
  </Page>
}
