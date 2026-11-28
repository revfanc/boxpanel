import { useId, useState } from 'react'
import { Link } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { ChoiceSelect } from '@/components/ChoiceSelect'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Confirm, Field, Notice, Panel, RadioCard } from '@/components/ResourceUI'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { RadioGroup } from '@/components/ui/radio-group'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'
import { api } from '@/lib/api'
import { expiry } from '@/lib/format'
import { queryKeys } from '@/lib/query'
import { serverExitQuery } from '@/lib/resources'
import { domainSchema } from '@/schemas/rules'
import type { ExitFailure, ExitPreset, ExternalProxy, ProxyProtocol, ServerExit } from '@/types/api'

const ex = t.exitServers
const presets = ['ai', 'streaming'] as const
const failures = ['block', 'direct'] as const
const domains = (value: string) => value.split(/[\s,，]+/).filter(Boolean).map((name) => name.toLowerCase().replace(/\.$/, ''))
const domainList = z.string().refine((value) => {
  const list = domains(value)
  return list.length <= 128 && list.every((name) => name.includes('.') && domainSchema.safeParse(name).success)
}, ex.domainsInvalid)
const policySchema = z.object({
  target: z.string().min(1, ex.targetInvalid),
  mode: z.enum(['split', 'all']),
  failure: z.enum(failures),
  presets: z.array(z.enum(presets)),
  domains: domainList,
  suffixes: domainList,
})
type PolicyValues = z.infer<typeof policySchema>

function policyValues(link: ServerExit['link']): PolicyValues {
  return {
    target: link?.external_proxy ? 'external' : link ? String(link.exit_server_id) : '',
    mode: link?.mode ?? 'split',
    failure: link?.failure ?? 'block',
    presets: link?.rules.presets ?? [...presets],
    domains: link?.rules.domains.join('\n') ?? '',
    suffixes: link?.rules.domain_suffixes.join('\n') ?? '',
  }
}

export function ServerExitTab({ serverID }: { serverID: number }) {
  const query = useQuery(serverExitQuery(serverID))
  if (query.isPending) return <Loading />
  if (!query.data) return <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
  const data = query.data
  const target = data.candidates.find((candidate) => candidate.id === data.link?.exit_server_id)
  const state = data.link?.status
  const hint = state === 'fallback' ? ex.fallbackHint : state === 'unavailable' ? ex.blockedHint
    : state === 'stale' ? ex.staleHint : state === 'upgrade_required' ? ex.needsUpgrade : state === 'policy_pending' ? ex.policyHint : undefined

  return <>
    {query.error && <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />}
    <Panel title={ex.title}>
      <div className="flex flex-wrap items-center gap-3">
        <Badge variant={state === 'unavailable' || state === 'fallback' ? 'destructive' : state === 'available' ? 'default' : 'secondary'}>
          {state ? ex.states[state] : ex.direct}
        </Badge>
        {data.link?.external_proxy && <span>{data.link.external_proxy.host} · {data.link.external_proxy.port}</span>}
        {target && <Link to={`/servers/${target.id}?tab=exit`} className="text-sm font-medium underline underline-offset-4">{target.name}</Link>}
        {!!data.link?.checked_at && <span className="text-sm text-muted-foreground">{ex.checked(expiry(data.link.checked_at))}</span>}
      </div>
      {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
      {!data.supported && <Notice>{ex.needsUpgrade}</Notice>}
      {(data.supported || data.link) && <ExitPolicyForm key={serverID} data={data} />}
    </Panel>
    {data.link && !data.link.external_proxy && data.address && <details className="space-y-3 rounded-xl border px-5 py-4">
      <summary className="cursor-pointer font-medium">{ex.network}</summary>
      <p className="text-sm">{ex.cloudFirewall(data.link.port, data.address)}</p>
    </details>}
  </>
}

type ProxyDraft = { type: ProxyProtocol; host: string; port: string; username: string; password: string }
const protocols = ['socks5', 'http', 'https'] as const
function proxyDefaults(saved?: ExternalProxy): ProxyDraft {
  return { type: saved?.type ?? 'socks5', host: saved?.host ?? '', port: saved ? String(saved.port) : '', username: saved?.username ?? '', password: '' }
}

function ExitPolicyForm({ data }: { data: ServerExit }) {
  const form = useForm<PolicyValues>({
    resolver: zodResolver(policySchema), values: policyValues(data.link),
    resetOptions: { keepDirtyValues: true },
  })
  const target = useWatch({ control: form.control, name: 'target' })
  const selected = data.candidates.find((candidate) => String(candidate.id) === target)
  const external = target === 'external'
  const savedProxy = data.link?.external_proxy
  const [draft, setDraft] = useState<ProxyDraft | null>(null)
  const proxy = draft ?? proxyDefaults(savedProxy)
  const reuse = !!savedProxy?.has_password && !proxy.password && proxy.host.trim().toLowerCase() === savedProxy.host &&
    Number(proxy.port) === savedProxy.port && proxy.type === savedProxy.type && proxy.username === savedProxy.username
  const proxyBody = { ...proxy, host: proxy.host.trim(), port: Number(proxy.port), use_saved_password: reuse }
  const proxyReady = !!proxyBody.host && Number.isInteger(proxyBody.port) && proxyBody.port > 0 && proxyBody.port <= 65535 &&
    Boolean(proxy.username) === Boolean(proxy.password || reuse)
  const probe = useOperation()
  const [ip, setIP] = useState('')
  function updateProxy(change: Partial<ProxyDraft>) {
    setDraft({ ...proxy, ...change })
    setIP('')
    probe.reset()
  }
  const protocolID = useId()
  const mode = useWatch({ control: form.control, name: 'mode' })
  const failure = useWatch({ control: form.control, name: 'failure' })
  const { errors } = form.formState
  const targetID = useId()
  const modeID = useId()
  const [policyOpen, setPolicyOpen] = useState(false)
  const [customOpen, setCustomOpen] = useState(Boolean(data.link?.rules.domains.length || data.link?.rules.domain_suffixes.length))
  const failureID = useId()
  const domainsID = useId()
  const suffixesID = useId()
  const operation = useOperation()
  const removal = useOperation()
  const proxyDisabled = operation.pending || removal.pending || probe.pending || !data.external_supported
  const [confirm, setConfirm] = useState(false)
  const client = useQueryClient()
  const path = `/servers/${data.server_id}/exit` as const
  const refresh = () => client.invalidateQueries({ queryKey: queryKeys.servers })
  const save = form.handleSubmit((value) => {
    const candidate = data.candidates.find((item) => item.id === Number(value.target))
    if (external ? !proxyReady || !data.external_supported : !candidate || candidate.unavailable_reason) {
      form.setError('target', { message: ex.targetInvalid })
      return
    }
    const body = {
      ...(external ? { external_proxy: proxyBody } : { exit_server_id: Number(value.target) }), mode: value.mode, failure: value.failure,
      rules: { presets: value.presets, domains: domains(value.domains), domain_suffixes: domains(value.suffixes) },
    }
    removal.reset()
    void operation.run(() => api(path, { method: 'PUT', body }), async () => {
      form.reset({ ...value, domains: body.rules.domains.join('\n'), suffixes: body.rules.domain_suffixes.join('\n') })
      await refresh()
      setDraft(null)
    })
  }, (issues) => {
    if (issues.domains || issues.suffixes) {
      form.setValue('mode', 'split', { shouldDirty: true })
      setPolicyOpen(true)
      setCustomOpen(true)
    }
  })

  return <>
    <form onSubmit={save} className="space-y-4" noValidate>
      {data.candidates.length === 0 && !data.external_supported && !data.link && <p className="text-sm text-muted-foreground">{ex.noCandidates}</p>}
      <div className="space-y-2">
        <Label htmlFor={targetID}>{ex.target}</Label>
        <Controller name="target" control={form.control} render={({ field }) => <ChoiceSelect
          id={targetID} className="w-full" value={field.value} onChange={(value) => { field.onChange(value); probe.reset(); setIP('') }} ref={field.ref}
          disabled={probe.pending || operation.pending || removal.pending || !data.supported} aria-invalid={!!errors.target}
          options={[{ value: '', label: ex.choose, disabled: true }, ...data.candidates.map((candidate) => ({
            value: String(candidate.id), disabled: !!candidate.unavailable_reason,
            label: candidate.unavailable_reason ? ex.optionUnavailable(candidate.name, ex.reasons[candidate.unavailable_reason]) : candidate.name,
          })), { value: 'external', label: data.external_supported ? ex.external : ex.externalUpgrade, disabled: !data.external_supported }]} />} />
        {errors.target && <p className="text-sm text-destructive" role="alert">{errors.target.message}</p>}
      </div>

      {!external && !data.address && <Notice>{ex.addressRequired}</Notice>}
      {external && <fieldset className="space-y-3" disabled={proxyDisabled}>
        <div className="space-y-2">
          <Label htmlFor={protocolID}>{ex.proxyProtocol}</Label>
          <ChoiceSelect id={protocolID} disabled={proxyDisabled} value={proxy.type} onChange={(type) => updateProxy({ type: type as ProxyProtocol })}
            options={protocols.map((value) => ({ value, label: value.toUpperCase() }))} />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={ex.proxyHost} value={proxy.host} onChange={(e) => updateProxy({ host: e.target.value })} />
          <Field label={ex.proxyPort} type="number" min={1} max={65535} value={proxy.port} onChange={(e) => updateProxy({ port: e.target.value })} />
          <Field label={ex.proxyUser} autoComplete="off" value={proxy.username} onChange={(e) => updateProxy({ username: e.target.value })} />
          <Field label={ex.proxyPassword} type="password" autoComplete="new-password" placeholder={reuse ? ex.savedPassword : undefined}
            value={proxy.password} onChange={(e) => updateProxy({ password: e.target.value })} />
        </div>
        <p className="text-sm text-muted-foreground">{ex.proxyAuth} {proxy.type === 'socks5' ? ex.proxyUDP : ex.proxyTCP}</p>
        <Button type="button" variant="outline" disabled={!proxyReady || probe.pending} onClick={() => {
          setIP('')
          void probe.run(() => api<{ ip: string }>(`${path}/test`, { method: 'POST', body: proxyBody }), (result) => setIP(result.ip))
        }}>{probe.pending ? ex.proxyTesting : ex.proxyTest}</Button>
        {ip && <p role="status">{ex.proxyIP(ip)}</p>}
        {probe.error != null && <ErrorNotice error={probe.error} />}
      </fieldset>}
      {(external || selected) && <details open={policyOpen} onToggle={(event) => setPolicyOpen(event.currentTarget.open)}>
        <summary className="cursor-pointer text-sm font-medium">{ex.policyOptions}</summary>
        <fieldset className="mt-3 min-w-0 space-y-4" disabled={operation.pending || removal.pending || !data.supported}>
          <div className="space-y-2">
            <Label id={modeID}>{ex.mode}</Label>
            <Controller name="mode" control={form.control} render={({ field }) => <RadioGroup aria-labelledby={modeID} value={field.value}
              onValueChange={field.onChange} disabled={operation.pending || removal.pending || !data.supported} className="sm:grid-cols-2">
              <RadioCard value="split" label={ex.split} hint={ex.splitHint} />
              <RadioCard value="all" label={ex.all} hint={ex.allHint} />
            </RadioGroup>} />
          </div>

          {mode === 'split' && <fieldset className="space-y-3 rounded-md border p-4">
            <legend className="px-1 text-sm font-medium">{ex.rules}</legend>
            <Controller name="presets" control={form.control} render={({ field }) => <div className="flex flex-wrap gap-4">
              {presets.map((preset: ExitPreset) => <Label key={preset} className="cursor-pointer">
                <Checkbox checked={field.value.includes(preset)} onCheckedChange={(checked) =>
                  field.onChange(checked ? [...field.value, preset] : field.value.filter((item) => item !== preset))}
                  disabled={operation.pending || removal.pending || !data.supported} />
                {ex.presets[preset]}
              </Label>)}
              {field.value.includes('ai') && <p className="w-full text-xs text-muted-foreground">{ex.aiRuleSource}</p>}
            </div>} />
            <details open={customOpen} onToggle={(event) => setCustomOpen(event.currentTarget.open)} className="space-y-3">
              <summary className="cursor-pointer text-sm font-medium">{ex.customDomains}</summary>
              <p className="text-xs text-muted-foreground">{ex.domainsHint}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor={domainsID}>{ex.domains}</Label>
                  <Textarea id={domainsID} rows={3} aria-invalid={!!errors.domains} {...form.register('domains')} />
                  {errors.domains && <p className="text-sm text-destructive" role="alert">{errors.domains.message}</p>}
                </div>
                <div className="space-y-2">
                  <Label htmlFor={suffixesID}>{ex.suffixes}</Label>
                  <Textarea id={suffixesID} rows={3} aria-invalid={!!errors.suffixes} {...form.register('suffixes')} />
                  {errors.suffixes && <p className="text-sm text-destructive" role="alert">{errors.suffixes.message}</p>}
                </div>
              </div>
            </details>
          </fieldset>}

          <div className="space-y-2">
            <Label htmlFor={failureID}>{ex.failure}</Label>
            <Controller name="failure" control={form.control} render={({ field }) => <ChoiceSelect
              id={failureID} className="w-full" value={field.value} onChange={field.onChange} ref={field.ref}
              disabled={operation.pending || removal.pending || !data.supported}
              options={failures.map((value: ExitFailure) => ({ value, label: ex.failures[value] }))} />} />
            {failure === 'direct' && <p className="text-sm text-muted-foreground">{ex.fallbackHint}</p>}
          </div>
        </fieldset>
      </details>}

      {operation.error != null && <ErrorNotice error={operation.error} />}
      {operation.success && <Notice>{ex.saved}</Notice>}
      {removal.success && <Notice>{ex.disabled}</Notice>}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={!data.supported || (external ? !data.external_supported || !proxyReady : !data.address || !selected || !!selected.unavailable_reason) || probe.pending || operation.pending || removal.pending}>
          {operation.pending ? t.common.saving : t.common.save}
        </Button>
        {data.link && <Confirm open={confirm} onOpenChange={setConfirm}
          title={ex.disable} description={ex.disableHint} pending={removal.pending} error={removal.error}
          trigger={<Button type="button" variant="outline" disabled={operation.pending || probe.pending}>{ex.disable}</Button>}
          onConfirm={() => {
            operation.reset()
            void removal.run(() => api(path, { method: 'DELETE' }), async () => {
              setConfirm(false)
              setDraft(null)
              setIP('')
              probe.reset()
              form.reset(policyValues(null))
              await refresh()
            })
          }} />}
      </div>
    </form>
  </>
}
