import { useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription, SheetTrigger, SheetClose } from '@/components/ui/sheet'
import { Label } from '@/components/ui/label'
import { Page, Panel, Field, Toggle, Submit, Notice, Empty } from '@/components/ResourceUI'
import { DomainTags } from '@/components/DomainTags'
import { Loading, ErrorNotice } from '@/components/Feedback'
import { api, APIError } from '@/lib/api'
import { rulesQuery, refreshResources } from '@/lib/resources'
import { queryKeys } from '@/lib/query'
import { useOperation } from '@/hooks/use-operation'
import { domainSchema, rulesSchema, rulesBody, type RulesFormValues } from '@/schemas/rules'
import type { RulesSettings, RulesPreview } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

function Preview() {
  const [open, setOpen] = useState(false)
  const [format, setFormat] = useState<'singbox' | 'clash'>('singbox')
  const preview = useQuery({ queryKey: [...queryKeys.rules, 'preview', format], queryFn: ({ signal }) => api<RulesPreview>(`/rules/preview?format=${format}`, { signal }), enabled: open, staleTime: 0, gcTime: 0 })
  const noUser = preview.error instanceof APIError && preview.error.status === 404 && preview.error.code === 'not_found'
  return <Sheet open={open} onOpenChange={setOpen}>
    <SheetTrigger asChild><Button variant="outline">{t.rules.preview}</Button></SheetTrigger>
    <SheetContent showCloseButton={false} className="w-full max-w-full overflow-y-auto sm:max-w-2xl">
      <SheetHeader><SheetTitle>{t.rules.preview}</SheetTitle><SheetDescription>{t.rules.previewHint}</SheetDescription></SheetHeader>
      <div className="min-w-0 space-y-4 px-4 pb-6">
        <div className="flex flex-wrap items-center gap-3"><Label htmlFor="preview-format">{t.rules.format}</Label><select id="preview-format" value={format} onChange={(event) => setFormat(event.target.value as 'singbox' | 'clash')} className="min-w-0 rounded-md border bg-background p-2"><option value="singbox">{t.users.singbox}</option><option value="clash">{t.users.clash}</option></select><SheetClose asChild><Button variant="outline">{t.common.close}</Button></SheetClose></div>
        {preview.isPending || preview.isFetching ? <Loading /> : noUser ? <Empty>{t.rules.noPreview}</Empty> : preview.isError ? <ErrorNotice error={preview.error} retry={() => { void preview.refetch() }} /> : <pre className="min-w-0 rounded-lg border bg-muted/30 p-4 font-mono text-xs leading-6 whitespace-pre-wrap break-all">{typeof preview.data?.config === 'string' ? preview.data.config : JSON.stringify(preview.data?.config, null, 2)}</pre>}
      </div>
    </SheetContent>
  </Sheet>
}

function RulesForm({ rules }: { rules: RulesSettings }) {
  const form = useForm<RulesFormValues>({ resolver: zodResolver(rulesSchema), defaultValues: { ...rules, direct_draft: '', proxy_draft: '' } })
  const values = useWatch({ control: form.control })
  const errors = form.formState.errors
  const operation = useOperation()
  const client = useQueryClient()
  const presetHints = { bypass_cn: t.rules.bypassHint, gfw_only: t.rules.gfwHint, global: t.rules.globalHint }
  return <form noValidate className="space-y-5" onSubmit={form.handleSubmit((values) => {
    void operation.run(() => api<RulesSettings>('/rules', { method: 'PUT', body: rulesBody(values) }), async (saved) => { form.reset({ ...saved, direct_draft: '', proxy_draft: '' }); client.setQueryData(queryKeys.rules, saved); await refreshResources(client) })
  })}>
    <fieldset disabled={operation.pending} className="min-w-0 space-y-5">
      <fieldset className="min-w-0"><legend className="mb-3 text-sm font-medium">{t.rules.preset}</legend><div className="grid min-w-0 gap-3 lg:grid-cols-3">
        {(['bypass_cn', 'gfw_only', 'global'] as const).map((preset) => <label key={preset} className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 ${values.preset === preset ? 'border-primary bg-muted/50' : ''}`}><input type="radio" value={preset} {...form.register('preset')} className="mt-1 shrink-0 accent-primary" /><span className="space-y-2"><span className="block font-medium">{t.rules[preset]}</span><span className="block text-sm leading-6 text-muted-foreground">{presetHints[preset]}</span></span></label>)}
      </div></fieldset>
      <Toggle label={t.rules.ads} checked={values.block_ads ?? false} onChange={(value) => form.setValue('block_ads', value, { shouldDirty: true })} />
      <Panel title={t.rules.direct} hint={t.rules.domainHint}>
        {(['direct', 'proxy'] as const).map((kind) => {
          const field = kind === 'direct' ? 'custom_direct' : 'custom_proxy'
          const draft = kind === 'direct' ? 'direct_draft' : 'proxy_draft'
          return <DomainTags key={kind} label={t.rules[kind]} values={values[field] ?? []} draft={values[draft] ?? ''} error={errors[draft]?.message || errors[field]?.message} setDraft={(value) => { form.setValue(draft, value, { shouldDirty: true }); form.clearErrors(draft) }} add={() => {
            const domain = form.getValues(draft).trim().toLowerCase()
            const domains = form.getValues(field)
            if (!domainSchema.safeParse(domain).success || domains.length >= 128 && !domains.includes(domain)) { form.setError(draft, { message: t.rules.domainInvalid }); return }
            form.setValue(field, [...new Set([...domains, domain])], { shouldDirty: true, shouldValidate: true }); form.setValue(draft, ''); form.clearErrors(draft)
          }} remove={(domain) => form.setValue(field, form.getValues(field).filter((value) => value !== domain), { shouldDirty: true, shouldValidate: true })} />
        })}
      </Panel>
      <Panel title={t.rules.dns}>
        <Field label={t.rules.remote} {...form.register('dns.remote')} error={errors.dns?.remote?.message} />
        <Field label={t.rules.local} {...form.register('dns.local')} error={errors.dns?.local?.message} />
        <Toggle label={t.rules.fakeip} checked={values.dns?.fakeip ?? false} onChange={(value) => form.setValue('dns.fakeip', value, { shouldDirty: true })} />
        <Toggle label={t.rules.ipv6} checked={values.ipv6 ?? false} onChange={(value) => form.setValue('ipv6', value, { shouldDirty: true })} />
      </Panel>
      <Panel title={t.rules.source} hint={t.rules.sourceHint}>
        <div className="space-y-2"><Label htmlFor="rule-source">{t.rules.source}</Label><select id="rule-source" className="w-full min-w-0 rounded-md border bg-background p-2 text-sm" {...form.register('source')}><option value="metacubex">MetaCubeX</option><option value="sagernet">SagerNet</option></select></div>
        <Field label={t.rules.mirror} hint={t.rules.mirrorHint} {...form.register('mirror')} error={errors.mirror?.message} />
        <Field label={t.rules.interval} hint={t.rules.intervalHint} {...form.register('update_interval')} error={errors.update_interval?.message} />
      </Panel>
      <Submit pending={operation.pending} />
    </fieldset>
    {operation.error != null && <ErrorNotice error={operation.error} />}
    {operation.success && <Notice>{t.common.saved}</Notice>}
  </form>
}
export function RulesPage() {
  const query = useQuery(rulesQuery)
  return <Page title={t.pages.rules} description={t.rules.description} actions={<Preview />}>
    {query.isPending ? <Loading /> : query.isError ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} /> : <RulesForm rules={query.data} />}
  </Page>
}
