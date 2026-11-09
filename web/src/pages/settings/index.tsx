import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import type { z } from 'zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Page, Panel, Field, Toggle, Submit, Notice, Confirm } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { settingsQuery, replaceSession, sessionQuery, queryKeys } from '@/lib/query'
import { refreshResources } from '@/lib/resources'
import { api } from '@/lib/api'
import { visitPanel } from '@/lib/location'
import { useOperation } from '@/hooks/use-operation'
import { settingsSchema, usernameSchema, passwordSchema } from '@/schemas/settings'
import type { PanelSettings, SettingsResult, PasswordInput } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

type SettingsForm = z.infer<typeof settingsSchema>
function PanelForm({ settings }: { settings: PanelSettings }) {
  const form = useForm<SettingsForm>({ resolver: zodResolver(settingsSchema), defaultValues: settings })
  const operation = useOperation()
  const blockBT = useWatch({ control: form.control, name: 'block_bittorrent' })
  const client = useQueryClient()
  const [confirmation, setConfirmation] = useState<SettingsForm | null>(null)
  const [redirect, setRedirect] = useState<SettingsResult | null>(null)
  useEffect(() => {
    if (!redirect) return
    const timer = window.setTimeout(() => visitPanel(redirect.panel_url), redirect.restart_after * 1000 + 500)
    return () => window.clearTimeout(timer)
  }, [redirect])
  function save(values: SettingsForm) {
    void operation.run(() => api<SettingsResult>('/settings', { method: 'PUT', body: values }), async (result) => {
      setConfirmation(null)
      form.reset(result)
      client.setQueryData(queryKeys.settings, result)
      if (result.restart_after > 0) { setRedirect(result); return }
      await refreshResources(client)
    })
  }
  const errors = form.formState.errors
  return <Panel title={t.settings.panel} hint={t.settings.addressHint}>
    <form className="space-y-5" noValidate onSubmit={form.handleSubmit((values) => {
      operation.reset()
      if (values.web_port !== settings.web_port || values.web_path !== settings.web_path || values.sub_path !== settings.sub_path) setConfirmation(values)
      else save(values)
    })}>
      <fieldset disabled={operation.pending || !!redirect} className="min-w-0 space-y-5">
        <Field label={t.settings.panelName} {...form.register('panel_name')} error={errors.panel_name?.message} />
        <Field label={t.settings.publicHost} hint={t.settings.hostHint} {...form.register('public_host')} error={errors.public_host?.message} />
        <div className="grid min-w-0 gap-4 sm:grid-cols-2"><Field label={t.settings.port} type="number" {...form.register('web_port', { valueAsNumber: true })} error={errors.web_port?.message} /><Field label={t.settings.webPath} hint={t.settings.pathHint} {...form.register('web_path')} error={errors.web_path?.message} /></div>
        <Field label={t.settings.subPath} {...form.register('sub_path')} error={errors.sub_path?.message} />
        <p className="text-sm text-muted-foreground">{t.settings.tls}: {settings.panel_tls_mode}</p>
        <Toggle label={t.settings.blockBT} checked={blockBT} onChange={(checked) => form.setValue('block_bittorrent', checked, { shouldDirty: true })} />
        <Submit pending={operation.pending} />
      </fieldset>
      {operation.error != null && !confirmation && <ErrorNotice error={operation.error} />}
      {operation.success && !redirect && <Notice>{t.common.saved}</Notice>}
      {redirect && <Notice>{t.settings.redirect}<a className="mt-2 block break-all underline" href={redirect.panel_url}>{redirect.panel_url}</a></Notice>}
    </form>
    <Confirm open={!!confirmation} onOpenChange={(open) => { if (!open) setConfirmation(null) }} title={t.settings.confirmTitle} description={<>{t.settings.confirmHint}<br />{t.settings.port}: {confirmation?.web_port}<br />{t.settings.webPath}: /{confirmation?.web_path}<br />{t.settings.subPath}: /{confirmation?.sub_path}</>} pending={operation.pending} error={operation.error} onConfirm={() => { if (confirmation) save(confirmation) }} />
  </Panel>
}

function AccountForms() {
  const session = useQuery(sessionQuery)
  const username = useForm<{ username: string }>({ resolver: zodResolver(usernameSchema), defaultValues: { username: session.data?.username ?? '' } })
  const password = useForm<PasswordInput>({ resolver: zodResolver(passwordSchema), defaultValues: { old_password: '', new_password: '' } })
  const operation = useOperation()
  const client = useQueryClient()
  const navigate = useNavigate()
  function done() { password.reset(); replaceSession(client, null); void navigate('/login', { replace: true }) }
  return <Panel title={t.settings.account} hint={t.settings.accountHint}>
    <form className="space-y-4" noValidate onSubmit={username.handleSubmit((values) => { void operation.run(() => api<void>('/auth/username', { method: 'PUT', body: values }), done) })}>
      <Field label={t.settings.username} autoComplete="username" {...username.register('username')} error={username.formState.errors.username?.message} disabled={operation.pending} />
      <Submit pending={operation.pending} label={t.settings.changeUsername} />
    </form>
    <form className="space-y-4 border-t pt-5" noValidate onSubmit={password.handleSubmit((values) => { void operation.run(() => api<void>('/auth/password', { method: 'PUT', body: values }), done) })}>
      <Field label={t.settings.oldPassword} type="password" autoComplete="current-password" {...password.register('old_password')} error={password.formState.errors.old_password?.message} disabled={operation.pending} />
      <Field label={t.settings.newPassword} type="password" autoComplete="new-password" hint={t.settings.passwordHint} {...password.register('new_password')} error={password.formState.errors.new_password?.message} disabled={operation.pending} />
      <Submit pending={operation.pending} label={t.settings.changePassword} />
    </form>
    {operation.error != null && <ErrorNotice error={operation.error} />}
  </Panel>
}
export function SettingsPage() {
  const settings = useQuery(settingsQuery)
  return <Page title={t.pages.settings} description={t.settings.description}>
    {settings.isPending ? <Loading /> : settings.isError ? <ErrorNotice error={settings.error} retry={() => { void settings.refetch() }} /> : <><PanelForm settings={settings.data} /><AccountForms /></>}
  </Page>
}
