import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQueryClient } from '@tanstack/react-query'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogTrigger } from '@/components/ui/dialog'
import { Field, Submit, Confirm } from '@/components/ResourceUI'
import { ErrorNotice } from '@/components/Feedback'
import { useOperation } from '@/hooks/use-operation'
import { api } from '@/lib/api'
import { refreshResources } from '@/lib/resources'
import { bytes, expiry } from '@/lib/format'
import { userSchema, userDefaults, userBody, type UserFormValues } from '@/schemas/user'
import type { User } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export function UserUsage({ user }: { user: User }) {
  const used = user.traffic_up + user.traffic_down
  const percent = user.traffic_limit > 0 ? Math.min(100, Math.max(0, used / user.traffic_limit * 100)) : 0
  const usage = `${bytes(used)} / ${user.traffic_limit ? bytes(user.traffic_limit) : t.common.unlimited}`
  return <div className="space-y-3 text-sm">
    <Badge variant={user.status === 'active' ? 'default' : 'secondary'}>{t.users[user.status]}</Badge>
    <p>{t.users.usage}: {usage}</p>
    <Progress value={percent} aria-label={t.users.usage} aria-valuenow={percent} aria-valuetext={usage} />
    <p className="text-muted-foreground">{t.users.expire}: {expiry(user.expire_at)}</p>
  </div>
}
function UserForm({ user, close, setBusy }: { user?: User; close: () => void; setBusy: (busy: boolean) => void }) {
  const form = useForm<UserFormValues>({ resolver: zodResolver(userSchema), defaultValues: userDefaults(user) })
  const operation = useOperation()
  const client = useQueryClient()
  const errors = form.formState.errors
  return <form className="min-w-0 space-y-4" noValidate onSubmit={form.handleSubmit((values) => {
    setBusy(true)
    void operation.run(() => api<User>(user ? `/users/${user.id}` : '/users', { method: user ? 'PUT' : 'POST', body: userBody(values) }), async () => { await refreshResources(client); close() }).finally(() => setBusy(false))
  })}>
    <fieldset disabled={operation.pending} className="min-w-0 space-y-4">
      <Field label={t.users.name} {...form.register('name')} error={errors.name?.message} />
      <Field label={t.common.remark} {...form.register('remark')} error={errors.remark?.message} />
      <Field label={t.users.limit} type="number" step="any" hint={t.users.limitHint} {...form.register('limit_gib', { valueAsNumber: true })} error={errors.limit_gib?.message} />
      <Field label={t.users.expire} type="datetime-local" step="1" hint={t.users.expireHint} {...form.register('expiry')} error={errors.expiry?.message} />
    </fieldset>
    {operation.error != null && <ErrorNotice error={operation.error} />}
    <div className="flex flex-wrap justify-end gap-2"><Button type="button" variant="outline" disabled={operation.pending} onClick={close}>{t.common.cancel}</Button><Submit pending={operation.pending} label={user ? t.common.save : t.common.create} /></div>
  </form>
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
type Action = 'delete' | 'disable' | 'reset-token' | 'reset-credentials'
const descriptions = { delete: t.users.deleteHint, disable: t.common.disableHint, 'reset-token': t.users.tokenHint, 'reset-credentials': t.users.credentialsHint }
const titles = { delete: t.users.deleteTitle, disable: t.common.disable, 'reset-token': t.users.resetToken, 'reset-credentials': t.users.resetCredentials }
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
  function ask(kind: Action) { operation.reset(); setAction(kind) }
  return <div className="space-y-3">
    <div className="flex flex-wrap gap-2"><UserEditor user={user} />
      <Button variant="outline" disabled={operation.pending} onClick={() => user.enabled ? ask('disable') : perform('enable')}>{operation.pending ? t.common.working : user.enabled ? t.common.disable : t.common.enable}</Button>
      {detail && <><Button variant="outline" disabled={operation.pending} onClick={() => ask('reset-token')}>{t.users.resetToken}</Button><Button variant="outline" disabled={operation.pending} onClick={() => ask('reset-credentials')}>{t.users.resetCredentials}</Button></>}
      <Button variant="destructive" disabled={operation.pending} onClick={() => ask('delete')}>{t.common.delete}</Button>
    </div>
    {operation.error != null && !action && <ErrorNotice error={operation.error} />}
    <Confirm open={!!action} onOpenChange={(open) => { if (!open) setAction(null) }} title={action ? titles[action] : ''} description={<>{user.name}<br />{action ? descriptions[action] : ''}</>} pending={operation.pending} error={operation.error} onConfirm={() => { if (action) perform(action) }} />
  </div>
}
