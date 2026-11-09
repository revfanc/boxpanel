import { useState } from 'react'
import { useNavigate, useParams } from 'react-router'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Page, Panel, Field, Toggle, Submit, Notice, BackLink, Empty } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { inboundQuery, refreshResources } from '@/lib/resources'
import { api } from '@/lib/api'
import { useOperation } from '@/hooks/use-operation'
import { inboundSchema, inboundDefaults, inboundBody, type InboundFormValues } from '@/schemas/inbound'
import type { Inbound, InboundSuggestion, Protocol, RealityTarget } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

type InboundFormProps = {
  protocol: Protocol
  source?: Inbound | InboundSuggestion
  onSaved?: (inbound: Inbound) => void | Promise<void>
  onBusy?: (busy: boolean) => void
  submitLabel?: string
  initialTargets?: RealityTarget[]
}
export function InboundForm({ protocol, source, onSaved, onBusy, submitLabel, initialTargets }: InboundFormProps) {
  const existing = source && 'id' in source ? source : undefined
  const form = useForm<InboundFormValues>({ resolver: zodResolver(inboundSchema), defaultValues: inboundDefaults(protocol, source) })
  const values = useWatch({ control: form.control })
  const errors = form.formState.errors
  const save = useOperation()
  const recommend = useOperation()
  const check = useOperation()
  const [targets, setTargets] = useState<RealityTarget[] | null>(initialTargets ?? null)
  const [checked, setChecked] = useState<{ target: RealityTarget; server: string; port: number } | null>(null)
  const client = useQueryClient()
  const navigate = useNavigate()
  const checkingServer = values.handshake_server || values.server_name || ''
  const checkResult = checked?.server === checkingServer && checked.port === values.handshake_port ? checked.target : null
  return <Panel title={protocol === 'vless_reality' ? t.inbounds.reality : t.inbounds.hysteria2}>
    <form className="space-y-5" noValidate onSubmit={form.handleSubmit((data) => {
      onBusy?.(true)
      void save.run(() => api<Inbound>(existing ? `/inbounds/${existing.id}` : '/inbounds', { method: existing ? 'PUT' : 'POST', body: inboundBody(data) }), async (inbound) => { await refreshResources(client); if (onSaved) await onSaved(inbound); else void navigate('/inbounds') }).finally(() => onBusy?.(false))
    })}>
      <fieldset disabled={save.pending} className="min-w-0 space-y-5">
        <Field label={t.inbounds.name} {...form.register('remark')} error={errors.remark?.message} />
        <Field label={t.inbounds.port} type="number" placeholder={existing ? undefined : t.inbounds.suggestHint} {...form.register('port', { setValueAs: (value: string) => value === '' ? undefined : Number(value) })} error={errors.port?.message} />
        {protocol === 'vless_reality' ? <>
          <Field label={t.inbounds.target} {...form.register('server_name', { onChange: (event: { target: { value: string } }) => {
            // Keep a new node's default handshake in sync until explicitly customized.
            const old = form.getValues('handshake_server')
            if (!existing && (!old || old === values.server_name)) form.setValue('handshake_server', event.target.value)
          } })} error={errors.server_name?.message} hint={t.inbounds.manualHint} />
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" disabled={recommend.pending} onClick={() => { void recommend.run(() => api<{ targets: RealityTarget[] }>('/reality/recommend'), (result) => setTargets(result.targets)) }}>{recommend.pending ? t.inbounds.recommending : t.inbounds.recommend}</Button>
            <Button type="button" variant="outline" disabled={check.pending || !checkingServer} onClick={() => {
              const server = checkingServer, port = form.getValues('handshake_port')
              void check.run(() => api<RealityTarget>('/reality/check', { method: 'POST', body: { server, port } }), (target) => setChecked({ target, server, port }))
            }}>{check.pending ? t.inbounds.checking : t.inbounds.check}</Button>
          </div>
          {recommend.error != null && <ErrorNotice error={recommend.error} />}
          {check.error != null && <ErrorNotice error={check.error} />}
          {targets?.length === 0 && <Empty>{t.inbounds.noTargets}</Empty>}
          {targets && targets.length > 0 && <div className="flex flex-wrap gap-2">{targets.map((target) => <Button key={target.server} type="button" variant="outline" className="h-auto max-w-full whitespace-normal text-left" onClick={() => { form.setValue('server_name', target.server, { shouldDirty: true, shouldValidate: true }); form.setValue('handshake_server', target.server, { shouldDirty: true }); form.setValue('handshake_port', 443) }}>{target.server} · {target.latency_ms} ms</Button>)}</div>}
          {checkResult && <Notice>{checkResult.ok ? `${t.inbounds.checkOK} · ${checkResult.latency_ms} ms` : checkResult.hint || t.inbounds.checkFailed}</Notice>}
        </> : <Field label={t.inbounds.sni} {...form.register('sni')} error={errors.sni?.message} />}
        <details className="rounded-lg border p-4" open={Object.keys(errors).some((key) => !['remark', 'port', 'server_name', 'sni'].includes(key)) || undefined}>
          <summary className="cursor-pointer text-sm font-medium">{t.common.advanced}</summary>
          <div className="mt-5 min-w-0 space-y-4">
            <Field label={t.inbounds.tag} {...form.register('tag')} error={errors.tag?.message} />
            <Toggle label={t.common.enabled} checked={values.enabled ?? true} onChange={(value) => form.setValue('enabled', value)} />
            {protocol === 'vless_reality' ? <>
              <Field label={t.inbounds.handshake} {...form.register('handshake_server')} error={errors.handshake_server?.message} />
              <Field label={t.inbounds.handshakePort} type="number" {...form.register('handshake_port', { valueAsNumber: true })} error={errors.handshake_port?.message} />
              <Field label={t.inbounds.fingerprint} {...form.register('fingerprint')} error={errors.fingerprint?.message} />
              <Field label={t.inbounds.shortIDs} {...form.register('short_ids')} error={errors.short_ids?.message} />
              {existing?.protocol === 'vless_reality' && <Field label={t.inbounds.publicKey} value={existing.options.public_key} readOnly />}
            </> : <>
              <div className="grid min-w-0 gap-4 sm:grid-cols-2"><Field label={t.inbounds.up} type="number" {...form.register('up_mbps', { valueAsNumber: true })} error={errors.up_mbps?.message} /><Field label={t.inbounds.down} type="number" {...form.register('down_mbps', { valueAsNumber: true })} error={errors.down_mbps?.message} /></div>
              <p className="text-xs text-muted-foreground">{t.inbounds.bandwidthHint}</p>
              <Field label={t.inbounds.obfs} type="password" autoComplete="new-password" disabled={values.clear_obfs} hint={t.inbounds.obfsHint} {...form.register('obfs_password')} error={errors.obfs_password?.message} />
              {existing && <Toggle label={t.inbounds.clearObfs} checked={values.clear_obfs ?? false} onChange={(value) => form.setValue('clear_obfs', value)} />}
              <Toggle label={t.inbounds.hopping} checked={values.hopping ?? false} onChange={(value) => form.setValue('hopping', value)} />
              <div className="grid min-w-0 gap-4 sm:grid-cols-2"><Field label={t.inbounds.start} type="number" {...form.register('start', { valueAsNumber: true })} error={errors.start?.message} /><Field label={t.inbounds.end} type="number" {...form.register('end', { valueAsNumber: true })} error={errors.end?.message} /></div>
            </>}
          </div>
        </details>
        <Submit pending={save.pending} label={submitLabel ?? (existing ? t.common.save : t.common.create)} />
      </fieldset>
      {save.error != null && <ErrorNotice error={save.error} />}
    </form>
  </Panel>
}

export function SuggestedForm({ protocol, autoRecommend = false, ...props }: Omit<InboundFormProps, 'source'> & { autoRecommend?: boolean }) {
  const suggestion = useQuery({ queryKey: ['inbound-suggest', protocol], queryFn: ({ signal }) => api<InboundSuggestion>('/inbounds/suggest', { method: 'POST', body: { protocol }, signal }), retry: false, staleTime: 0, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false })
  const targets = useQuery({ queryKey: ['reality', 'recommend'], queryFn: ({ signal }) => api<{ targets: RealityTarget[] }>('/reality/recommend', { signal }), enabled: autoRecommend && protocol === 'vless_reality', retry: false, refetchOnWindowFocus: false })
  return <>
    {suggestion.isPending || (autoRecommend && protocol === 'vless_reality' && targets.isPending) ? <Loading label={t.inbounds.suggesting} /> : <>
      {suggestion.isError && <><ErrorNotice error={suggestion.error} /><Notice>{t.inbounds.suggestHint}</Notice></>}
      <InboundForm protocol={protocol} source={suggestion.data} initialTargets={targets.data?.targets} {...props} />
    </>}
  </>
}
export function NewInboundPage() {
  const [protocol, setProtocol] = useState<Protocol | null>(null)
  return <Page title={t.pages.newInbound} actions={<BackLink to="/inbounds" />}>
    <fieldset className="min-w-0"><legend className="mb-3 text-sm font-medium">{t.inbounds.protocol}</legend><div className="grid gap-4 sm:grid-cols-2">
      {(['vless_reality', 'hysteria2'] as const).map((value) => <label key={value} className={`flex cursor-pointer items-start gap-3 rounded-xl border p-5 ${protocol === value ? 'border-primary bg-muted/50' : ''}`}><input type="radio" name="protocol" value={value} checked={protocol === value} onChange={() => setProtocol(value)} className="mt-1 shrink-0 accent-primary" /><span className="space-y-2"><span className="block font-medium">{value === 'vless_reality' ? t.inbounds.reality : t.inbounds.hysteria2}</span><span className="block text-sm leading-6 text-muted-foreground">{value === 'vless_reality' ? t.inbounds.realityHint : t.inbounds.hysteriaHint}</span></span></label>)}
    </div></fieldset>
    {protocol && <SuggestedForm key={protocol} protocol={protocol} />}
  </Page>
}
export function EditInboundPage() {
  const { id = '' } = useParams()
  const inbound = useQuery(inboundQuery(id))
  return <Page title={t.pages.editInbound} actions={<BackLink to="/inbounds" />}>
    {inbound.isPending ? <Loading /> : inbound.isError ? <ErrorNotice error={inbound.error} retry={() => { void inbound.refetch() }} /> : <InboundForm key={inbound.data.id} protocol={inbound.data.protocol} source={inbound.data} />}
  </Page>
}
