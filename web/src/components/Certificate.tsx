import { useState } from 'react'
import { Link } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Notice, Panel } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { certQuery, queryKeys } from '@/lib/query'
import { api } from '@/lib/api'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'

const date = (seconds: number) => seconds ? new Date(seconds * 1000).toLocaleString() : t.cert.never

export function CertificatePanel() {
  const certificate = useQuery(certQuery)
  const operation = useOperation()
  const client = useQueryClient()
  const [action, setAction] = useState<'issue' | 'self'>('issue')
  const status = certificate.data
  function change(next: 'issue' | 'self') {
    setAction(next)
    void operation.run(() => next === 'issue' ? api('/cert/issue', { method: 'POST' }) : api('/settings', { method: 'PUT', body: { panel_tls_mode: 'self_signed' } }), async () => {
      await Promise.all([client.invalidateQueries({ queryKey: queryKeys.cert }), client.invalidateQueries({ queryKey: queryKeys.settings })])
    })
  }
  return <Panel title={t.cert.title} hint={t.cert.hint}>
    {certificate.isPending ? <Loading /> : certificate.isError ? <ErrorNotice error={certificate.error} retry={() => { void certificate.refetch() }} /> : status && <>
      <dl className="grid gap-3 text-sm sm:grid-cols-2">
        {[[t.cert.current, t.cert.types[status.type]], [t.cert.host, status.host || t.cert.never], [t.cert.issuer, status.issuer || t.cert.never], [t.cert.expires, date(status.not_after)], [t.cert.lastAttempt, date(status.last_attempt)], [t.cert.lastSuccess, date(status.last_success)]].map(([label, value]) => <div key={label} className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className="mt-1 break-words font-medium">{value}</dd></div>)}
      </dl>
      {status.issuing && <Notice>{t.cert.issuing}</Notice>}
      {status.last_error ? <div role="alert" className="space-y-2 rounded-lg border border-destructive p-4 text-sm"><p className="font-medium">{t.cert.failed}</p><p>{status.hint}</p><p>{t.cert.retryAt}: {date(status.next_attempt)}</p></div> : status.last_success > 0 && <Notice>{t.cert.succeeded}</Notice>}
      {status.type === 'http' ? <Notice>{t.cert.dev}</Notice> : <div className="flex flex-wrap gap-3"><Button className="h-auto whitespace-normal" disabled={operation.pending || status.issuing} onClick={() => change('issue')}>{operation.pending || status.issuing ? t.cert.issuing : t.cert.issue}</Button><Button variant="outline" disabled={operation.pending || status.mode === 'self_signed'} onClick={() => change('self')}>{t.cert.selfSigned}</Button></div>}
    </>}
    {operation.error != null && <ErrorNotice error={operation.error} />}
    {operation.success && <Notice>{action === 'issue' ? t.cert.accepted : t.cert.switched}</Notice>}
  </Panel>
}

export function CertificateWarning() {
  const certificate = useQuery(certQuery)
  if (!certificate.data?.warning) return null
  return <div role="alert" className="mb-5 space-y-2 rounded-lg border border-destructive p-4 text-sm leading-6"><p className="font-semibold">{t.cert.expiring}</p><p>{certificate.data.hint}</p><Link to="/settings" className="underline">{t.cert.settings}</Link></div>
}

export function SetupCertificate({ welcome = false }: { welcome?: boolean }) {
  const certificate = useQuery(certQuery)
  if (certificate.isPending) return <Loading />
  if (certificate.isError) return <ErrorNotice error={certificate.error} retry={() => { void certificate.refetch() }} />
  if (certificate.data.type === 'http') return null
  return <Notice>{certificate.data.type === 'self_signed' ? (welcome ? t.setup.certificate : t.setup.tlsHint) : t.cert.acmeActive}{certificate.data.issuing && ` ${t.cert.issuing}`}{certificate.data.hint && ` ${certificate.data.hint}`}</Notice>
}
