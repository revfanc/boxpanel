import { useForm, useWatch } from 'react-hook-form'
import { useId } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { scenarioSchema } from '@/schemas/setup'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Field, Notice, Panel } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { SuggestedForm } from '@/pages/inbounds/editor'
import { UserForm } from '@/pages/users/components'
import { checkQuery, inboundsQuery, usersQuery } from '@/lib/resources'
import { api } from '@/lib/api'
import { useOperation } from '@/hooks/use-operation'
import type { BBR, SetupStatus } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export type StepProps = { next: () => Promise<void>; setBusy: (busy: boolean) => void }
export function CheckStep({ next, setBusy }: StepProps) {
  const check = useQuery(checkQuery)
  const bbr = useOperation()
  return <>
    <Button variant="outline" disabled={check.isFetching || bbr.pending} onClick={() => { void check.refetch() }}>{t.common.refresh}</Button>
    {check.isPending ? <Loading /> : check.isError ? <ErrorNotice error={check.error} retry={() => { void check.refetch() }} /> : <>
      <ul className="space-y-3">{check.data.items.map((item) => <li key={item.id} className="space-y-2 rounded-lg border p-4"><p className="font-medium">{({ ok: '✅', warn: '⚠️', error: '❌' })[item.level]} {t.setup.levels[item.level]} · {item.title}</p><p className="text-sm leading-6 text-muted-foreground">{item.hint}</p>{item.action === 'enable_bbr' && !check.data.bbr.enabled && <Button disabled={bbr.pending} onClick={() => {
        setBusy(true)
        void bbr.run(() => api<BBR>('/system/bbr', { method: 'POST' }), async () => { await check.refetch() }).finally(() => setBusy(false))
      }}>{bbr.pending ? t.common.working : t.setup.bbr}</Button>}</li>)}</ul>
      <Notice>{t.setup.checkHint}</Notice>
    </>}
    {bbr.error != null && <ErrorNotice error={bbr.error} />}
    <Button disabled={!check.data || check.isFetching || check.isError || bbr.pending || check.data.items.some((item) => item.level === 'error')} onClick={() => { void next() }}>{t.setup.next}</Button>
  </>
}
export function ScenarioStep({ status, save }: { status: SetupStatus; save: (patch: Partial<SetupStatus>) => Promise<void> }) {
  const form = useForm({ resolver: zodResolver(scenarioSchema), defaultValues: { hasDomain: !!status.domain, domain: status.domain, hysteria2: status.hysteria2 } })
  const checkboxID = useId()
  const hasDomain = useWatch({ control: form.control, name: 'hasDomain' })
  return <form noValidate className="space-y-5" onSubmit={form.handleSubmit((values) => save({ step: 'node', domain: values.hasDomain ? values.domain.trim() : '', hysteria2: values.hysteria2 }))}>
    <div className="grid gap-4 sm:grid-cols-2">{[false, true].map((hasDomain) => <label key={String(hasDomain)} className="flex cursor-pointer items-start gap-3 rounded-xl border p-5"><input type="radio" name="scenario" defaultChecked={hasDomain === !!status.domain} onChange={() => form.setValue('hasDomain', hasDomain)} className="mt-1 shrink-0" /><span><span className="block font-medium">{hasDomain ? t.setup.hasDomain : t.setup.noDomain}</span><span className="mt-2 block text-sm leading-6 text-muted-foreground">{hasDomain ? t.setup.domainHint : t.setup.noDomainHint}</span></span></label>)}</div>
    {hasDomain && <Field label={t.setup.domain} {...form.register('domain')} error={form.formState.errors.domain?.message} />}
    <label htmlFor={checkboxID} className="flex items-start gap-3 rounded-lg border p-4 text-sm leading-6"><input id={checkboxID} aria-labelledby={`${checkboxID}-label`} type="checkbox" className="mt-1 shrink-0" {...form.register('hysteria2')} /><span id={`${checkboxID}-label`}>{t.setup.hy2}</span></label>
    <Button type="submit" disabled={form.formState.isSubmitting}>{form.formState.isSubmitting ? t.common.working : t.setup.next}</Button>
  </form>
}
export function NodeStep({ status, next, setBusy }: StepProps & { status: SetupStatus }) {
  const nodes = useQuery(inboundsQuery)
  if (nodes.isPending) return <Loading />
  if (nodes.isError) return <ErrorNotice error={nodes.error} retry={() => { void nodes.refetch() }} />
  const active = nodes.data.filter((node) => node.enabled)
  const missing = !active.some((node) => node.protocol === 'vless_reality') ? 'vless_reality' : status.hysteria2 && !active.some((node) => node.protocol === 'hysteria2') ? 'hysteria2' : null
  return <>
    <Notice>{t.setup.nodeHint}</Notice>
    {active.length > 0 && <Panel title={t.setup.existingNodes} hint={t.setup.reuse}>{active.map((node) => <p key={node.id} className="text-sm">{node.remark || node.tag} · {node.port}/{node.protocol === 'vless_reality' ? 'TCP' : 'UDP'}</p>)}</Panel>}
    {missing ? <SuggestedForm key={missing} protocol={missing} autoRecommend onBusy={setBusy} submitLabel={t.setup.createNext} onSaved={async () => { if (missing === 'hysteria2' || !status.hysteria2 || active.some((node) => node.protocol === 'hysteria2')) await next() }} /> : <Button onClick={() => { void next() }}>{t.setup.next}</Button>}
  </>
}
export function UserStep({ next, setBusy }: StepProps) {
  const users = useQuery(usersQuery)
  if (users.isPending) return <Loading />
  if (users.isError) return <ErrorNotice error={users.error} retry={() => { void users.refetch() }} />
  const existing = users.data.find((user) => user.status === 'active')
  return <Panel title={t.setup.steps.user} hint={t.setup.userHint}>
    <UserForm key={existing?.id ?? 'new'} user={existing} defaultName="me" setBusy={setBusy} onSaved={next} submitLabel={existing ? t.setup.saveNext : t.setup.createNext} />
  </Panel>
}
