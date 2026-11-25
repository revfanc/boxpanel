import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CopyField } from '@/components/CopyField'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Confirm, Notice } from '@/components/ResourceUI'
import { Button } from '@/components/ui/button'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'
import { api } from '@/lib/api'
import { installCommand } from '@/pages/servers/install-command'

type Status = {
  phase: 'idle' | 'issued' | 'frozen' | 'incoming' | 'active' | 'retired' | 'unavailable'
  target_url?: string
  expires_at?: number
  migrated: number
  total: number
  core_stopped: boolean
  waiting?: { id: number; name: string }[]
}
type Grant = { token: string; master_pin: string; source_url: string; version: string; expires_at: number }

const statusKey = ['migration', 'status']
const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`

export function MasterMigration({ publicHost }: { publicHost: string }) {
  const client = useQueryClient()
  const status = useQuery({ queryKey: statusKey, queryFn: ({ signal }) => api<Status>('/migration', { signal }), refetchInterval: 3000 })
  const operation = useOperation()
  const [confirm, setConfirm] = useState(false)
  const [grant, setGrant] = useState<Grant | null>(null)
  const ipMode = publicHost.includes(':') || /^(?:\d{1,3}\.){3}\d{1,3}$/.test(publicHost)
  const modeHint = ipMode ? t.migration.ipHint : t.migration.domainHint
  const data = status.data
  const issueAllowed = data && ['idle', 'issued', 'active'].includes(data.phase)
  const showGrant = grant && data?.phase === 'issued' && data.expires_at === grant.expires_at

  function issue() {
    void operation.run(() => api<Grant>('/migration/token', { method: 'POST', body: { confirm: true } }), (result) => {
      setGrant(result)
      setConfirm(false)
      client.setQueryData<Status>(statusKey, { phase: 'issued', expires_at: result.expires_at, migrated: 0, total: 0, core_stopped: false })
    })
  }

  return <section className="space-y-3 border-t pt-5">
    <h3 className="font-medium">{t.migration.title}</h3>
    {ipMode && <Notice>{modeHint}</Notice>}
    {status.isPending && <Loading />}
    {status.isError && <ErrorNotice error={status.error} retry={() => { void status.refetch() }} />}
    {data?.phase === 'unavailable' && <Notice>{t.migration.unavailable}</Notice>}
    {data?.phase === 'frozen' && <Notice>{t.migration.frozen}</Notice>}
    {data?.phase === 'incoming' && <Notice>{t.migration.incoming}</Notice>}
    {data?.phase === 'issued' && !showGrant && <Notice>{t.migration.issued}</Notice>}
    {data?.phase === 'retired' && <Notice>{t.migration.retired}</Notice>}
    {data && ['active', 'retired'].includes(data.phase) && <div className="space-y-2">
      <p className="text-sm font-medium">{t.migration.progress(data.migrated, data.total)}</p>
      {data.phase === 'retired' && data.target_url && <a className="block text-sm underline" href={data.target_url}>{t.migration.newPanel}</a>}
      {data.phase === 'retired' && !data.core_stopped && <Notice>{t.migration.stopping}</Notice>}
      {data.phase === 'retired' && data.core_stopped && data.migrated === data.total && <Notice>{t.migration.shutdown}</Notice>}
      {data.phase === 'active' && !!data.waiting?.length && data.target_url && <>
        <p className="text-sm">{t.migration.waiting} · {data.waiting.map((node) => node.name).join('、')}</p>
        <p className="text-sm text-muted-foreground">{t.migration.manualHint}</p>
        <CopyField label={t.migration.manual} value={`boxpanel set-master ${quote(data.target_url)}`} />
      </>}
    </div>}
    {issueAllowed && <Button variant="outline" disabled={operation.pending} onClick={() => { operation.reset(); setConfirm(true) }}>{t.migration.generate}</Button>}
    {showGrant && <>
      <Notice>{t.migration.commandHint}</Notice>
      <CopyField label={t.migration.command} value={installCommand(grant.version, `--migrate-from ${quote(grant.source_url)} --token ${quote(grant.token)} --master-pin ${quote(grant.master_pin)}`)} />
    </>}
    <Confirm open={confirm} onOpenChange={setConfirm} title={t.migration.confirmTitle} description={<>{t.migration.confirmHint}<br />{modeHint}</>} pending={operation.pending} error={operation.error} onConfirm={issue} />
  </section>
}
