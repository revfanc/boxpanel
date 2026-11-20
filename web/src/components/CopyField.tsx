import { useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { zhCN as t } from '@/i18n/zh-CN'

export function CopyField({ label, value }: { label: string; value: string }) {
  const id = useId()
  const [state, setState] = useState<'idle' | 'pending' | 'copied' | 'failed'>('idle')
  return <div className="min-w-0 space-y-2">
    <Label htmlFor={id}>{label}</Label>
    <div className="flex min-w-0 flex-wrap items-start gap-2">
      <Textarea id={id} readOnly value={value} rows={2} className="flex-1 basis-48 resize-y bg-muted/30 p-3 font-mono text-xs leading-5 break-all md:text-xs dark:bg-muted/30" onFocus={(event) => event.currentTarget.select()} />
      <Button type="button" variant="outline" disabled={state === 'pending'} aria-label={`${t.common.copy} ${label}`} onClick={() => {
        setState('pending')
        void (async () => { try { await navigator.clipboard.writeText(value); setState('copied') } catch { setState('failed') } })()
      }}>{state === 'pending' ? t.common.copying : state === 'copied' ? t.common.copied : t.common.copy}</Button>
    </div>
    {state === 'failed' && <p role="alert" className="text-sm text-destructive">{t.common.copyFailed}</p>}
    {state === 'copied' && <p role="status" className="sr-only">{t.common.copied}</p>}
  </div>
}
