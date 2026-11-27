import { useId, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { RadioGroup } from '@/components/ui/radio-group'
import { ChoiceSelect } from '@/components/ChoiceSelect'
import { Page, Panel, Field, Toggle, Submit, Notice, BackLink, Empty, RadioCard } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { inboundQuery, refreshResources, serversQuery } from '@/lib/resources'
import { api } from '@/lib/api'
import { inboundListPath } from '@/lib/navigation'
import { useOperation } from '@/hooks/use-operation'
import { inboundSchema, inboundDefaults, inboundBody, type InboundFormValues } from '@/schemas/inbound'
import type { Inbound, InboundSuggestion, Protocol, RealityTarget, Server } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

type InboundFormProps = {
  protocol: Protocol
  source?: Inbound | InboundSuggestion
  onSaved?: (inbound: Inbound) => void | Promise<void>
  onBusy?: (busy: boolean) => void
  submitLabel?: string
  initialTargets?: RealityTarget[]
  suggestionError?: unknown
  suggestionHint?: string
  target?: Server
}
// Only fields used by server generation/firewall affect the restart reminder.
function appliedFields(values: Partial<InboundFormValues>) {
  if (!values.enabled) return [false]
  return [values.enabled, values.port, values.tag,
    ...(values.protocol === 'vless_reality'
      ? [values.server_name, values.handshake_server || values.server_name, values.handshake_port, values.short_ids?.split(',').map((id) => id.trim())]
      : [values.sni, values.up_mbps, values.down_mbps, values.obfs_password, values.clear_obfs, values.hopping, ...(values.hopping ? [values.start, values.end] : [])]),
  ]
}

export function InboundForm({ protocol, source, onSaved, onBusy, submitLabel, initialTargets, suggestionError, suggestionHint, target }: InboundFormProps) {
  const existing = source && 'id' in source ? source : undefined
  const form = useForm<InboundFormValues>({ resolver: zodResolver(inboundSchema), defaultValues: inboundDefaults(protocol, source) })
  const values = useWatch({ control: form.control })
  const remote = target ? !target.is_local : !!existing?.key_status
  const moving = !!existing && !!target && target.id !== existing.server_id
  const changesConfig = moving || !existing || JSON.stringify(appliedFields(values)) !== JSON.stringify(appliedFields(inboundDefaults(protocol, existing)))
  const errors = form.formState.errors
  const save = useOperation()
  const recommend = useOperation()
  const check = useOperation()
  const [targets, setTargets] = useState<RealityTarget[] | null>(initialTargets ?? null)
  const [checked, setChecked] = useState<{ target: RealityTarget; server: string; port: number } | null>(null)
  const client = useQueryClient()
  const navigate = useNavigate()
  const checkingServer = values.handshake_server || values.server_name || ''
  const displayError = save.error ?? check.error ?? recommend.error ?? (recommend.success ? null : suggestionError)
  const checkResult = checked?.server === checkingServer && checked.port === values.handshake_port ? checked.target : null
  return <Panel title={protocol === 'vless_reality' ? t.inbounds.reality : t.inbounds.hysteria2}>
    <form className="space-y-5" noValidate onSubmit={form.handleSubmit((data) => {
      onBusy?.(true)
      const body = inboundBody(remote || moving ? { ...data, short_ids: '' } : data)
      if (target) body.server_id = target.id
      void save.run(() => api<Inbound>(existing ? `/inbounds/${existing.id}` : '/inbounds', { method: existing ? 'PUT' : 'POST', body }), async (inbound) => { await refreshResources(client); if (onSaved) await onSaved(inbound); else void navigate('/inbounds') }).finally(() => onBusy?.(false))
    })}>
      <fieldset disabled={save.pending} className="min-w-0 space-y-5">
        <Field label={t.inbounds.name} {...form.register('remark')} error={errors.remark?.message} />
        <Field label={t.inbounds.port} type="number" placeholder={existing ? undefined : t.inbounds.suggestHint} {...form.register('port', { setValueAs: (value: string) => value === '' ? undefined : Number(value) })} error={errors.port?.message} />
        {protocol === 'vless_reality' ? <>
          <Field label={t.inbounds.target} {...form.register('server_name', { onChange: (event: { target: { value: string } }) => {
            // Keep a new node's default handshake in sync until explicitly customized.
            const old = form.getValues('handshake_server')
            if (!existing && (!old || old === values.server_name)) form.setValue('handshake_server', event.target.value)
          } })} error={errors.server_name?.message} hint={remote ? undefined : t.inbounds.manualHint} />
          {!remote && <><div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" disabled={recommend.pending} onClick={() => { void recommend.run(() => api<{ targets: RealityTarget[] }>('/reality/recommend'), (result) => setTargets(result.targets)) }}>{recommend.pending ? t.inbounds.recommending : t.inbounds.recommend}</Button>
            <Button type="button" variant="outline" disabled={check.pending || !checkingServer} onClick={() => {
              const server = checkingServer, port = form.getValues('handshake_port')
              void check.run(() => api<RealityTarget>('/reality/check', { method: 'POST', body: { server, port } }), (target) => setChecked({ target, server, port }))
            }}>{check.pending ? t.inbounds.checking : t.inbounds.check}</Button>
          </div>
          {targets?.length === 0 && ((!suggestionHint && !suggestionError) || recommend.success) && <Empty>{t.inbounds.noTargets}</Empty>}
          {targets && targets.length > 0 && <div className="flex flex-wrap gap-2">{targets.map((target) => <Button key={target.server} type="button" variant="outline" className="h-auto max-w-full whitespace-normal text-left" onClick={() => { form.setValue('server_name', target.server, { shouldDirty: true, shouldValidate: true }); form.setValue('handshake_server', target.server, { shouldDirty: true }); form.setValue('handshake_port', 443) }}>{target.server} · {target.latency_ms} ms</Button>)}</div>}
          {checkResult && <Notice>{checkResult.ok ? `${t.inbounds.checkOK} · ${checkResult.latency_ms} ms` : checkResult.hint || t.inbounds.checkFailed}</Notice>}
          </>}
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
              {!remote && !moving && <Field label={t.inbounds.shortIDs} {...form.register('short_ids')} error={errors.short_ids?.message} />}
              {!moving && existing?.protocol === 'vless_reality' && existing.options.public_key && <Field label={t.inbounds.publicKey} value={existing.options.public_key} readOnly />}
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
        {remote && <Notice>{t.inbounds.remoteCheck}</Notice>}
        {moving && <Notice>{t.inbounds.moveHint}</Notice>}
        {changesConfig && <p className="text-sm text-muted-foreground">{remote ? t.inbounds.remoteApply : t.common.applyHint}</p>}
        <Submit pending={save.pending} label={submitLabel ?? (existing ? t.common.save : t.common.create)} />
      </fieldset>
      {displayError != null ? <ErrorNotice error={displayError} /> : !recommend.success && suggestionHint && <Notice>{suggestionHint}</Notice>}
    </form>
  </Panel>
}

export function SuggestedForm({ protocol, autoRecommend = false, ...props }: Omit<InboundFormProps, 'source'> & { autoRecommend?: boolean }) {
  const server_id = props.target?.id
  const suggestion = useQuery({ queryKey: ['inbound-suggest', protocol, server_id], queryFn: ({ signal }) => api<InboundSuggestion>('/inbounds/suggest', { method: 'POST', body: { protocol, server_id }, signal }), retry: false, staleTime: 0, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false })
  const targets = useQuery({ queryKey: ['reality', 'recommend'], queryFn: ({ signal }) => api<{ targets: RealityTarget[] }>('/reality/recommend', { signal }), enabled: autoRecommend && protocol === 'vless_reality', retry: false, refetchOnWindowFocus: false })
  return <>
    {suggestion.isPending || (autoRecommend && protocol === 'vless_reality' && targets.isPending) ? <Loading label={t.inbounds.suggesting} /> : <>
      <InboundForm protocol={protocol} source={suggestion.data} initialTargets={targets.data?.targets} suggestionError={suggestion.error} suggestionHint={suggestion.data?.hint} {...props} />
    </>}
  </>
}
function ServerSelect({ servers, selected, onChange }: { servers: Server[]; selected?: number | null; onChange: (server: Server) => void }) {
  const id = useId()
  const choices = servers.filter((server) => server.is_local || server.status === 'online' || server.status === 'offline')
  if (choices.length < 2 && (selected === undefined || choices.some((server) => server.id === selected))) return null
  const current = selected === undefined ? choices.find((server) => server.is_local)?.id : selected
  const available = choices.some((server) => server.id === current)
  const options = choices.map((server) => ({ value: String(server.id), label: server.is_local ? server.name : `${server.name} · ${t.servers.statuses[server.status]}` }))
  return <div className="space-y-2">
    <Label htmlFor={id}>{t.inbounds.server}</Label>
    <ChoiceSelect id={id} className="max-w-full" value={available ? String(current) : 'unknown'}
      options={available ? options : [{ value: 'unknown', label: t.common.unknown, disabled: true }, ...options]} onChange={(value) => {
      const server = choices.find((row) => String(row.id) === value)
      if (server) onChange(server)
    }} />
  </div>
}

export function NewInboundPage() {
  const servers = useQuery(serversQuery)
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const [protocol, setProtocol] = useState<Protocol | null>(null)
  const requested = params.get('target') ?? params.get('server') ?? params.get('filter')
  const target = servers.data?.find((server) => String(server.id) === requested && server.status !== 'pending')
  const unavailable = requested !== null && !target
  const returnTo = inboundListPath(params)
  return <Page title={t.pages.newInbound} actions={<BackLink to={returnTo} />}>
    {servers.isError && <ErrorNotice error={servers.error} retry={() => { void servers.refetch() }} />}
    {servers.data && <ServerSelect servers={servers.data} selected={unavailable ? null : target?.id} onChange={(server) => {
      const next = new URLSearchParams(params)
      next.set('target', String(server.id))
      setParams(next, { replace: true })
    }} />}
    {unavailable && (servers.isPending ? <Loading /> : servers.data && <Notice>{t.inbounds.serverUnavailable}</Notice>)}
    <fieldset disabled={unavailable} className="min-w-0">
      <legend id="inbound-protocol" className="mb-3 text-sm font-medium">{t.inbounds.protocol}</legend>
      <RadioGroup aria-labelledby="inbound-protocol" value={protocol} onValueChange={(value) => {
        if (value === 'vless_reality' || value === 'hysteria2') setProtocol(value)
      }} disabled={unavailable} className="gap-4 sm:grid-cols-2">
        <RadioCard value="vless_reality" label={t.inbounds.reality} hint={t.inbounds.realityHint} className="p-5" />
        <RadioCard value="hysteria2" label={t.inbounds.hysteria2} hint={t.inbounds.hysteriaHint} className="p-5" />
      </RadioGroup>
    </fieldset>
    {protocol && !unavailable && <SuggestedForm key={`${protocol}:${target?.id ?? ''}`} protocol={protocol} target={target} onSaved={() => navigate(returnTo)} />}
  </Page>
}
export function EditInboundPage() {
  const { id = '' } = useParams()
  const [params] = useSearchParams()
  const returnTo = inboundListPath(params)
  const inbound = useQuery(inboundQuery(id))
  return <Page title={t.pages.editInbound} actions={<BackLink to={returnTo} />}>
    {inbound.isPending ? <Loading /> : inbound.isError ? <ErrorNotice error={inbound.error} retry={() => { void inbound.refetch() }} /> : <EditableInbound key={inbound.data.id} inbound={inbound.data} returnTo={returnTo} />}
  </Page>
}


function EditableInbound({ inbound, returnTo }: { inbound: Inbound; returnTo: string }) {
  const servers = useQuery(serversQuery)
  const navigate = useNavigate()
  const [target, setTarget] = useState<Server>()
  return <>
    {servers.isError && <ErrorNotice error={servers.error} retry={() => { void servers.refetch() }} />}
    {servers.data && <ServerSelect servers={servers.data} selected={target?.id ?? inbound.server_id} onChange={setTarget} />}
    <InboundForm protocol={inbound.protocol} source={inbound} target={target}
      onSaved={() => navigate(returnTo)} />
  </>
}
