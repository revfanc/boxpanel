import { useId, useState } from 'react'
import { useNavigate } from 'react-router'
import { Controller, useForm, useWatch, type Control } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { Progress } from '@/components/ui/progress'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogTrigger } from '@/components/ui/dialog'
import { Field, Submit, Confirm } from '@/components/ResourceUI'
import { ErrorNotice } from '@/components/Feedback'
import { useOperation } from '@/hooks/use-operation'
import { api } from '@/lib/api'
import { refreshResources, serversQuery } from '@/lib/resources'
import { bytes, expiry } from '@/lib/format'
import { userSchema, userDefaults, userBody, type UserFormValues } from '@/schemas/user'
import type { Server, User } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

function userEligible(user: User, now: number) {
  return user.enabled && (user.expire_at === 0 || user.expire_at > now) &&
    (user.traffic_limit === 0 || user.traffic_up < user.traffic_limit && user.traffic_down < user.traffic_limit - user.traffic_up)
}

export function UserUsage({ user }: { user: User }) {
  const used = user.traffic_up + user.traffic_down
  const percent = user.traffic_limit > 0 ? Math.min(100, Math.max(0, used / user.traffic_limit * 100)) : 0
  const usage = `${bytes(used)} / ${user.traffic_limit ? bytes(user.traffic_limit) : t.common.unlimited}`
  return <div className="space-y-3 text-sm">
    <Badge variant={user.status === 'active' ? 'default' : 'secondary'}>{t.users[user.status]}</Badge>
    <p>{t.users.usage}: {usage}</p>
    <p className="text-muted-foreground">{t.users.usageHint}</p>
    <Progress value={percent} aria-label={t.users.usage} aria-valuenow={percent} aria-valuetext={usage} />
    <p className="text-muted-foreground">{t.users.expire}: {expiry(user.expire_at)}</p>
    {!user.all_servers && <p>{t.users.servers}: {t.users.serverAccess(user.server_ids.length)}</p>}
    <p>{t.users.resetDay}: {user.traffic_reset_day ? t.users.resetOnDay(user.traffic_reset_day) : t.users.resetDisabled}</p>
    {user.traffic_reset_at > 0 && <p>{t.users.resetAt}: {new Date(user.traffic_reset_at * 1000).toISOString()}</p>}
  </div>
}
export function UserForm({ user, close, setBusy, onSaved, defaultName, submitLabel }: {
  user?: User; close?: () => void; setBusy?: (busy: boolean) => void
  onSaved?: (user: User) => void | Promise<void>; defaultName?: string; submitLabel?: string
}) {
  const form = useForm<UserFormValues>({ resolver: zodResolver(userSchema), defaultValues: { ...userDefaults(user), name: user?.name ?? defaultName ?? '' } })
  const values = useWatch({ control: form.control })
  const candidate = userSchema.safeParse(values)
  const next = candidate.success ? userBody(candidate.data) : undefined
  const [now] = useState(() => Math.floor(Date.now() / 1000))
  const active = user ? userEligible(user, now) : false
  const nextActive = user && next ? userEligible({ ...user, ...next }, now) : active
  const servers = useQuery(serversQuery).data ?? []
  const accessChanged = !!user && !!next && (next.all_servers !== user.all_servers ||
    !next.all_servers && !sameIDs(next.server_ids ?? [], user.server_ids))
  const changesConfig = !user || active !== nextActive || (active && (accessChanged || next?.name !== undefined && next.name !== user.name))
  const operation = useOperation()
  const client = useQueryClient()
  const errors = form.formState.errors
  // The choice only matters with several servers, but a narrowed user must be able to go back.
  const showAccess = servers.length > 1 || (user ? !user.all_servers : false)
  return <form className="min-w-0 space-y-4" noValidate onSubmit={form.handleSubmit((values) => {
    setBusy?.(true)
    const known = new Set(servers.map((server) => server.id))
    const body = userBody(servers.length ? { ...values, server_ids: values.server_ids.filter((id) => known.has(id)) } : values)
    void operation.run(() => api<User>(user ? `/users/${user.id}` : '/users', { method: user ? 'PUT' : 'POST', body }), async (saved) => { await refreshResources(client); if (onSaved) await onSaved(saved); else close?.() }).finally(() => setBusy?.(false))
  })}>
    <fieldset disabled={operation.pending} className="min-w-0 space-y-4">
      <Field label={t.users.name} {...form.register('name')} error={errors.name?.message} />
      <Field label={t.common.remark} {...form.register('remark')} error={errors.remark?.message} />
      <Field label={t.users.limit} type="number" step="any" hint={t.users.limitHint} {...form.register('limit_gib', { valueAsNumber: true })} error={errors.limit_gib?.message} />
      <Field label={t.users.expire} type="datetime-local" step="1" hint={t.users.expireHint} {...form.register('expiry')} error={errors.expiry?.message} />
      <Field label={t.users.resetDay} type="number" min="0" max="28" step="1" hint={t.users.resetDayHint} {...form.register('traffic_reset_day', { valueAsNumber: true })} error={errors.traffic_reset_day?.message} />
      {showAccess && <ServerAccess control={form.control} servers={servers} error={errors.server_ids?.message} />}
    </fieldset>
    {changesConfig && <p className="text-sm text-muted-foreground">{t.common.applyHint}</p>}
    {operation.error != null && <ErrorNotice error={operation.error} />}
    <div className="flex flex-wrap justify-end gap-2">{close && <Button type="button" variant="outline" disabled={operation.pending} onClick={close}>{t.common.cancel}</Button>}<Submit pending={operation.pending} label={submitLabel ?? (user ? t.common.save : t.common.create)} /></div>
  </form>
}
function sameIDs(a: number[], b: number[]) {
  const set = new Set(a)
  return set.size === new Set(b).size && b.every((id) => set.has(id))
}

function ServerAccess({ control, servers, error }: { control: Control<UserFormValues>; servers: Server[]; error?: string }) {
  const id = useId()
  const all = useWatch({ control, name: 'all_servers' })
  return <div role="group" aria-labelledby={`${id}-label`} className="space-y-2">
    <p id={`${id}-label`} className="text-sm font-medium">{t.users.servers}</p>
    <Controller name="all_servers" control={control} render={({ field }) => (
      <Label htmlFor={`${id}-all`} className="cursor-pointer gap-3 font-normal">
        <Checkbox id={`${id}-all`} name={field.name} checked={field.value} onCheckedChange={(checked) => field.onChange(checked === true)} onBlur={field.onBlur} ref={field.ref} />
        <span>{t.users.allServers}</span>
      </Label>
    )} />
    {!all && <Controller name="server_ids" control={control} render={({ field, fieldState }) => (
      <div className="space-y-2 rounded-lg border p-3">
        {servers.map((server) => <Label key={server.id} htmlFor={`${id}-${server.id}`} className="cursor-pointer gap-3 font-normal">
          <Checkbox id={`${id}-${server.id}`} checked={field.value.includes(server.id)} aria-invalid={fieldState.invalid}
            onCheckedChange={(checked) => field.onChange(checked === true ? [...field.value.filter((value) => value !== server.id), server.id] : field.value.filter((value) => value !== server.id))} onBlur={field.onBlur} />
          <span className="min-w-0 break-all">{server.name}{server.is_local ? '' : ` · ${server.public_host}`}</span>
        </Label>)}
      </div>
    )} />}
    <p className="text-sm text-muted-foreground">{t.users.serversHint}</p>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
  </div>
}

export function UserEditor({ user }: { user?: User }) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  return <Dialog open={open} onOpenChange={(value) => { if (!busy) setOpen(value) }}>
    <DialogTrigger asChild><Button variant={user ? 'outline' : 'default'}>{user ? t.common.edit : t.users.create}</Button></DialogTrigger>
    <DialogContent showCloseButton={false} className="max-h-[90svh] max-w-[calc(100vw-2rem)] overflow-y-auto sm:max-w-lg">
      <DialogHeader><DialogTitle>{user ? t.users.edit : t.users.create}</DialogTitle><DialogDescription>{t.users.description}</DialogDescription></DialogHeader>
      <UserForm user={user} close={() => setOpen(false)} setBusy={setBusy} />
    </DialogContent>
  </Dialog>
}
type Action = 'reset-traffic' | 'delete' | 'disable' | 'reset-token' | 'reset-credentials'
const descriptions = { 'reset-traffic': t.users.resetTrafficHint, delete: t.users.deleteHint, disable: t.common.disableHint, 'reset-token': t.users.tokenHint, 'reset-credentials': t.users.credentialsHint }
const titles = { 'reset-traffic': t.users.resetTraffic, delete: t.users.deleteTitle, disable: t.common.disable, 'reset-token': t.users.resetToken, 'reset-credentials': t.users.resetCredentials }
export function UserActions({ user, detail = false }: { user: User; detail?: boolean }) {
  const [action, setAction] = useState<Action | null>(null)
  const operation = useOperation()
  const client = useQueryClient()
  const navigate = useNavigate()
  function perform(kind: Action | 'enable') {
    const suffix = kind === 'delete' ? '' : kind === 'disable' || kind === 'enable' ? '/toggle' : `/${kind}`
    void operation.run(() => api<User | void>(`/users/${user.id}${suffix}`, { method: kind === 'delete' ? 'DELETE' : 'POST', ...(kind === 'disable' || kind === 'enable' ? { body: { enabled: kind === 'enable' } } : {}) }), async () => {
      // Never show/copy a revoked token while refreshed links are pending.
      const linksKey = ['users', String(user.id), 'links']
      await client.cancelQueries({ queryKey: linksKey })
      await client.resetQueries({ queryKey: linksKey })
      setAction(null)
      if (kind === 'delete' && detail) void navigate('/users')
      await refreshResources(client)
    })
  }
  const changesConfig = action === 'delete' || action === 'disable' || action === 'reset-credentials'
  const [now] = useState(() => Math.floor(Date.now() / 1000))
  const active = userEligible(user, now)
  function ask(kind: Action) { operation.reset(); setAction(kind) }
  return <div className="space-y-3">
    <div className="flex flex-wrap gap-2"><UserEditor user={user} />
      <Button variant="outline" disabled={operation.pending} onClick={() => user.enabled ? ask('disable') : perform('enable')}>{operation.pending ? t.common.working : user.enabled ? t.common.disable : t.common.enable}</Button>
      {detail && <><Button variant="outline" disabled={operation.pending} onClick={() => ask('reset-traffic')}>{t.users.resetTraffic}</Button><Button variant="outline" disabled={operation.pending} onClick={() => ask('reset-token')}>{t.users.resetToken}</Button><Button variant="outline" disabled={operation.pending} onClick={() => ask('reset-credentials')}>{t.users.resetCredentials}</Button></>}
      <Button variant="destructive" disabled={operation.pending} onClick={() => ask('delete')}>{t.common.delete}</Button>
    </div>
    {!user.enabled && userEligible({ ...user, enabled: true }, now) && <p className="text-sm text-muted-foreground">{t.common.applyHint}</p>}
    {operation.error != null && !action && <ErrorNotice error={operation.error} />}
    <Confirm open={!!action} onOpenChange={(open) => { if (!open) setAction(null) }} title={action ? titles[action] : ''} description={<>{user.name}<br />{action ? descriptions[action] : ''}{changesConfig && active && <><br />{t.common.applyHint}</>}</>} pending={operation.pending} error={operation.error} onConfirm={() => { if (action) perform(action) }} />
  </div>
}
