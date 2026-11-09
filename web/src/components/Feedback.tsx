import { AlertCircle, LoaderCircle } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { APIError } from '@/lib/api'
import { zhCN } from '@/i18n/zh-CN'

export function ErrorNotice({ error, retry }: { error: unknown; retry?: () => void }) {
  const known = error instanceof APIError ? error : null
  return (
    <Alert variant="destructive" role="alert" className="min-w-0 text-left [overflow-wrap:anywhere]">
      <AlertCircle aria-hidden="true" />
      <AlertTitle>{known?.message ?? zhCN.errors.request}</AlertTitle>
      <AlertDescription className="min-w-0 space-y-2">
        <p>{known?.hint ?? zhCN.errors.retryHint}</p>
        {known?.retryAfter && <p>{zhCN.login.retryAfter(known.retryAfter)}</p>}
        {known?.detail && <details className="w-full min-w-0">
          <summary className="cursor-pointer">{zhCN.common.detail}</summary>
          <pre className="mt-2 max-h-56 overflow-y-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 text-xs">{known.detail}</pre>
        </details>}
        {retry && <Button variant="outline" size="sm" onClick={retry}>{zhCN.common.retry}</Button>}
      </AlertDescription>
    </Alert>
  )
}

export function Loading({ label = zhCN.common.loading }: { label?: string }) {
  return <div role="status" className="flex items-center justify-center gap-2 p-8 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />{label}</div>
}
