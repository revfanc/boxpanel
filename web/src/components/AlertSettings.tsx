import { useId } from 'react'
import { Link } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Field, Notice, Panel, Submit, Toggle } from '@/components/ResourceUI'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { useOperation } from '@/hooks/use-operation'
import { alertOverviewQuery, alertSettingsQuery } from '@/lib/alerts'
import { api } from '@/lib/api'
import { zhCN as t } from '@/i18n/zh-CN'
import type { AlertConfig, AlertSettings, AlertKind, NotificationChannel, NotificationChannelSettings } from '@/types/api'

const a = t.alerts
const eventKinds = Object.keys(a.event) as AlertKind[]
const channelKinds = ['bark', 'server_chan', 'telegram'] as const
const billingModes = ['out', 'both', 'max', 'in'] as const
const configSchema = z.object({
  interface: z.string().trim(),
  quota_gb: z.number({ error: a.invalidQuota }).int(a.invalidQuota).min(0, a.invalidQuota).max(9223372036, a.invalidQuota),
  billing_day: z.number({ error: a.invalidDay }).int(a.invalidDay).min(1, a.invalidDay).max(28, a.invalidDay),
  billing_mode: z.enum(billingModes),
  events: z.record(z.enum(eventKinds), z.boolean()),
})

function BillingSettings({ settings }: { settings: AlertSettings }) {
  const form = useForm<AlertConfig>({ resolver: zodResolver(configSchema), defaultValues: settings.config })
  const events = useWatch({ control: form.control, name: 'events' })
  const { errors } = form.formState
  const operation = useOperation()
  const client = useQueryClient()
  const devices = useId()
  const mode = useId()

  return <Panel title={a.settings} hint={a.accounting}>
    <form className="space-y-4" noValidate onSubmit={form.handleSubmit((values) => {
      void operation.run(() => api<AlertConfig>('/alerts/settings', { method: 'PUT', body: values }), async (config) => {
        form.reset(config)
        client.setQueryData(alertSettingsQuery.queryKey, (current) => current && ({ ...current, config }))
        await client.invalidateQueries({ queryKey: alertOverviewQuery.queryKey })
      })
    })}>
      <fieldset disabled={operation.pending} className="min-w-0 space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={a.quota} type="number" min={0} hint={a.quotaHint} error={errors.quota_gb?.message} {...form.register('quota_gb', { valueAsNumber: true })} />
          <Field label={a.billingDay} type="number" min={1} max={28} hint={a.dayHint} error={errors.billing_day?.message} {...form.register('billing_day', { valueAsNumber: true })} />
          <div>
            <Field label={a.network} placeholder={a.networkAuto} hint={a.networkHint} list={devices} {...form.register('interface')} />
            <datalist id={devices}>{settings.interfaces.map((name) => <option key={name} value={name} />)}</datalist>
          </div>
          <div className="space-y-2">
            <Label htmlFor={mode}>{a.mode}</Label>
            <select id={mode} className="h-9 w-full rounded-md border bg-background px-3 text-sm" {...form.register('billing_mode')}>
              {billingModes.map((value) => <option key={value} value={value}>{a.modes[value]}</option>)}
            </select>
          </div>
        </div>
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">{a.thresholds}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {eventKinds.map((kind) => <Toggle key={kind} label={a.event[kind]} checked={events[kind]} onChange={(checked) => form.setValue(`events.${kind}`, checked, { shouldDirty: true })} />)}
          </div>
        </fieldset>
        <Submit pending={operation.pending} label={a.saveSettings} />
      </fieldset>
      {operation.error != null && <ErrorNotice error={operation.error} />}
      {operation.success && <Notice>{a.settingsSaved}</Notice>}
    </form>
  </Panel>
}

function ChannelForm({ kind, saved }: { kind: NotificationChannel; saved: NotificationChannelSettings }) {
  const form = useForm({ defaultValues: { enabled: saved.enabled, url: saved.url ?? '', credential: '', chat_id: '' } })
  const enabled = useWatch({ control: form.control, name: 'enabled' })
  const client = useQueryClient()
  const save = useOperation()
  const test = useOperation()
  const busy = save.pending || test.pending
  const canTest = !busy && !form.formState.isDirty && !!saved.credential_mask && (kind !== 'telegram' || !!saved.chat_id_mask)

  function saveChannel(values: { enabled: boolean; url: string; credential: string; chat_id: string }) {
    test.reset()
    const body = {
      enabled: values.enabled,
      ...(kind === 'bark' && values.url.trim() !== saved.url ? { url: values.url } : {}),
      ...(values.credential.trim() ? { credential: values.credential } : {}),
      ...(kind === 'telegram' && values.chat_id.trim() ? { chat_id: values.chat_id } : {}),
    }
    void save.run(() => api<NotificationChannelSettings>(`/alerts/channels/${kind}`, { method: 'PUT', body }), (channel) => {
      form.reset({ enabled: channel.enabled, url: channel.url ?? '', credential: '', chat_id: '' })
      client.setQueryData(alertSettingsQuery.queryKey, (current) => current && ({
        ...current, channels: { ...current.channels, [kind]: channel },
      }))
    })
  }

  return <form className="space-y-3 rounded-lg border p-4" onSubmit={form.handleSubmit(saveChannel)}>
    <h3 className="font-medium">{a.channelNames[kind]}</h3>
    <fieldset disabled={busy} className="min-w-0 space-y-3">
      <Toggle label={a.channelEnabled(a.channelNames[kind])} checked={enabled} onChange={(checked) => form.setValue('enabled', checked, { shouldDirty: true })} />
      {kind === 'bark' && <Field label={a.barkURL} hint={a.barkHint} {...form.register('url')} />}
      <Field type="password" autoComplete="new-password" label={a.credentials[kind]} placeholder={saved.credential_mask} hint={a.secretHint} {...form.register('credential')} />
      {kind === 'telegram' && <Field type="password" autoComplete="off" label={a.chatID} placeholder={saved.chat_id_mask} hint={a.chatHint} {...form.register('chat_id')} />}
      <div className="flex flex-wrap gap-2">
        <Submit pending={save.pending} label={a.channelSave(a.channelNames[kind])} />
        <Button type="button" variant="outline" disabled={!canTest} onClick={() => {
          save.reset()
          void test.run(() => api(`/alerts/channels/${kind}/test`, { method: 'POST' }), () => client.invalidateQueries({ queryKey: alertOverviewQuery.queryKey }))
        }}>{test.pending ? t.common.working : a.sendTest}</Button>
      </div>
    </fieldset>
    {!canTest && !busy && !test.success && <p className="text-xs text-muted-foreground">{a.reenterHint}</p>}
    {(save.error ?? test.error) != null && <ErrorNotice error={save.error ?? test.error} />}
    {save.success && <Notice>{a.channelSaved}</Notice>}
    {test.success && <Notice>{a.testQueued} <Link to="/" className="underline">{t.pages.home}</Link></Notice>}
  </form>
}

export function AlertSettingsPanel() {
  const query = useQuery(alertSettingsQuery)
  if (query.isPending) return <Panel title={a.settings}><Loading /></Panel>
  if (query.isError) return <Panel title={a.settings}><ErrorNotice error={query.error} retry={() => { void query.refetch() }} /></Panel>
  return <>
    <BillingSettings settings={query.data} />
    <Panel title={a.channels} hint={a.channelsHint}>
      <div className="grid items-start gap-4 xl:grid-cols-3">
        {channelKinds.map((kind) => <ChannelForm key={kind} kind={kind} saved={query.data.channels[kind]} />)}
      </div>
    </Panel>
  </>
}
