import { useId, useRef, type ReactNode, type ComponentProps } from 'react'
import { Link } from 'react-router'
import { LoaderCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction, AlertDialogTrigger } from '@/components/ui/alert-dialog'
import { ErrorNotice } from '@/components/Feedback'
import { zhCN as t } from '@/i18n/zh-CN'

export function Page({ title, description, actions, children }: { title: string; description?: string; actions?: ReactNode; children: ReactNode }) {
  return <section className="min-w-0 space-y-6 [overflow-wrap:anywhere]">
    <div className="flex flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-2"><h1 className="text-2xl font-semibold tracking-tight">{title}</h1>{description && <p className="text-sm leading-6 text-muted-foreground">{description}</p>}</div>{actions}</div>
    {children}
  </section>
}
export function Panel({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return <Card className="min-w-0"><CardHeader><CardTitle>{title}</CardTitle>{hint && <CardDescription className="leading-6">{hint}</CardDescription>}</CardHeader><CardContent className="min-w-0 space-y-4">{children}</CardContent></Card>
}
export function Empty({ children }: { children: ReactNode }) {
  return <div className="rounded-xl border border-dashed bg-muted/20 px-5 py-10 text-center text-sm leading-7 text-muted-foreground">{children}</div>
}
export function Notice({ children }: { children: ReactNode }) {
  return <p role="status" className="rounded-lg border bg-muted/40 p-4 text-sm leading-6">{children}</p>
}
export function BackLink({ to }: { to: string }) { return <Button variant="outline" asChild><Link to={to}>{t.common.back}</Link></Button> }
export function Field({ label, hint, error, id: given, ...props }: ComponentProps<typeof Input> & { label: string; hint?: string; error?: string }) {
  const auto = useId()
  const id = given ?? auto
  return <div className="min-w-0 space-y-2"><Label htmlFor={id}>{label}</Label><Input {...props} id={id} aria-invalid={!!error} aria-describedby={error ? `${id}-error` : hint ? `${id}-hint` : undefined} />{hint && <p id={`${id}-hint`} className="text-xs leading-5 text-muted-foreground">{hint}</p>}{error && <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{error}</p>}</div>
}
export function Toggle({ label, checked, onChange, disabled }: { label: string; checked: boolean; onChange: (checked: boolean) => void; disabled?: boolean }) {
  const id = useId()
  return <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-3"><Label htmlFor={id} className="leading-5">{label}</Label><Switch id={id} checked={checked} onCheckedChange={onChange} disabled={disabled} /></div>
}
export function Submit({ pending, label = t.common.save }: { pending: boolean; label?: string }) {
  return <Button type="submit" disabled={pending}>{pending && <LoaderCircle aria-hidden="true" className="animate-spin" />}{pending ? t.common.saving : label}</Button>
}
export function Confirm({ open, onOpenChange, title, description, pending, error, onConfirm, trigger }: {
  open: boolean; onOpenChange: (open: boolean) => void; title: string; description: ReactNode; pending: boolean; error?: unknown; onConfirm: () => void; trigger?: ReactNode
}) {
  const previousFocus = useRef<HTMLElement | null>(null)
  return <AlertDialog open={open} onOpenChange={(value) => { if (!pending) onOpenChange(value) }}>
    {trigger && <AlertDialogTrigger asChild>{trigger}</AlertDialogTrigger>}
    <AlertDialogContent className="max-h-[90svh] max-w-[calc(100vw-2rem)] overflow-y-auto sm:max-w-lg" onOpenAutoFocus={() => { previousFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null }} onCloseAutoFocus={(event) => {
      // List actions share a controlled dialog without a Radix Trigger.
      event.preventDefault()
      if (previousFocus.current?.isConnected) previousFocus.current.focus()
      else document.getElementById('main-content')?.focus()
    }}>
      <AlertDialogHeader><AlertDialogTitle>{title}</AlertDialogTitle><AlertDialogDescription className="leading-6 [overflow-wrap:anywhere]">{description}</AlertDialogDescription></AlertDialogHeader>
      {error != null && <ErrorNotice error={error} />}
      <AlertDialogFooter><AlertDialogCancel disabled={pending}>{t.common.cancel}</AlertDialogCancel><AlertDialogAction disabled={pending} onClick={(event) => { event.preventDefault(); if (!pending) onConfirm() }}>{pending ? t.common.working : t.common.confirm}</AlertDialogAction></AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
}
