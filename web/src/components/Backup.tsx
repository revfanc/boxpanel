import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { Panel, Field, Submit, Notice, Confirm } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { useOperation } from '@/hooks/use-operation'
import type { BackupStatus } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'
import { MasterMigration } from '@/components/MasterMigration'

const passphrase = z.string().refine((value) => Array.from(value).length >= 12 && new TextEncoder().encode(value).length <= 1024, t.backup.passphraseError)
const schema = z.object({ passphrase })
type Values = z.infer<typeof schema>

export function BackupPanel({ publicHost }: { publicHost?: string }) {
  const status = useQuery({ queryKey: ['backup', 'status'], queryFn: ({ signal }) => api<BackupStatus>('/backup/status', { signal }), refetchInterval: 60_000 })
  const download = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { passphrase: '' } })
  const restore = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { passphrase: '' } })
  const operation = useOperation()
  const [file, setFile] = useState<File | null>(null)
  const [fileError, setFileError] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const disabled = operation.pending || restoring
  async function exportBackup(values: Values) {
    await operation.run(() => api<Blob>('/backup', { method: 'POST', body: values, responseType: 'blob' }), (blob) => {
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url; anchor.download = `boxpanel-backup-${new Date().toISOString().replaceAll(':', '-')}.tar.gz.enc`
      document.body.append(anchor); anchor.click(); anchor.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 1000)
      download.reset()
    })
  }
  function restoreBackup() {
    if (!file) return
    void operation.run(async () => {
      const form = new FormData()
      form.append('file', file); form.append('passphrase', restore.getValues('passphrase')); form.append('confirm', 'true')
      return api<{ accepted: boolean }>('/restore', { method: 'POST', body: form })
    }, () => { setConfirm(false); setRestoring(true); restore.reset(); setFile(null) })
  }
  return <Panel title={t.backup.title} hint={t.backup.hint}>
    <Notice>{t.backup.localWarning}</Notice>
    {status.isPending ? <Loading /> : status.isError ? <ErrorNotice error={status.error} retry={() => { void status.refetch() }} /> : <div className="space-y-1 text-sm">
      <p>{t.backup.automatic}: {status.data.last_attempt ? `${new Date(status.data.last_attempt * 1000).toISOString()} · ${status.data.result === 'success' ? t.backup.success : t.backup.failed}` : t.backup.never}</p>
      {!!status.data.last_success && <p>{t.backup.lastSuccess}: {new Date(status.data.last_success * 1000).toISOString()}</p>}
    </div>}
    <form className="space-y-4" noValidate onSubmit={download.handleSubmit((values) => { void exportBackup(values) })}>
      <Field label={t.backup.passphrase} type="password" autoComplete="off" hint={t.backup.passphraseHint} disabled={disabled} {...download.register('passphrase')} error={download.formState.errors.passphrase?.message} />
      <fieldset disabled={disabled}><Submit pending={operation.pending} label={t.backup.download} /></fieldset>
    </form>
    <form className="space-y-4 border-t pt-5" noValidate onSubmit={restore.handleSubmit(() => {
      if (!file || !file.size || file.size > 64 * 1024 * 1024) { setFileError(true); return }
      operation.reset(); setConfirm(true)
    })}>
      <Field label={t.backup.file} type="file" accept=".enc" disabled={disabled} hint={t.backup.fileHint} error={fileError ? t.backup.fileError : undefined} onChange={(event) => { setFile(event.target.files?.[0] ?? null); setFileError(false) }} />
      <Field label={t.backup.restorePassphrase} type="password" autoComplete="off" disabled={disabled} {...restore.register('passphrase')} error={restore.formState.errors.passphrase?.message} />
      <Button type="submit" variant="destructive" disabled={disabled}>{t.backup.restore}</Button>
    </form>
    {!confirm && operation.error != null && <ErrorNotice error={operation.error} />}
    {restoring && <Notice>{t.backup.restoring} <Link className="underline" to="/login">{t.backup.login}</Link></Notice>}
    <Confirm open={confirm} onOpenChange={setConfirm} title={t.backup.confirmTitle} description={t.backup.confirmHint} pending={operation.pending} error={operation.error} onConfirm={restoreBackup} />
    {publicHost && <MasterMigration publicHost={publicHost} />}
  </Panel>
}
