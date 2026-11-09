import { useId } from 'react'
import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Field } from '@/components/ResourceUI'
import { zhCN as t } from '@/i18n/zh-CN'

export function DomainTags({ label, values, draft, setDraft, add, remove, error }: {
  label: string; values: string[]; draft: string; setDraft: (value: string) => void; add: () => void; remove: (value: string) => void; error?: string
}) {
  const id = useId()
  return <div className="min-w-0 space-y-3">
    <div className="flex min-w-0 flex-wrap items-start gap-2"><div className="min-w-0 flex-1 basis-48"><Field id={id} label={label} value={draft} error={error} onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter' && !event.nativeEvent.isComposing) { event.preventDefault(); add() } }} /></div><Button type="button" className="mt-6" variant="outline" onClick={add} aria-label={`${t.rules.add} ${label}`}>{t.rules.add}</Button></div>
    <div className="flex min-w-0 flex-wrap gap-2" aria-label={label}>
      {values.length === 0 ? <p className="text-xs text-muted-foreground">{t.rules.noDomains}</p> : values.map((domain) => <Badge key={domain} variant="secondary" className="max-w-full whitespace-normal break-all py-1 pl-2 pr-1"><span className="min-w-0">{domain}</span><button type="button" aria-label={t.common.remove(domain)} onClick={() => remove(domain)} className="ml-1 shrink-0 rounded p-1 hover:bg-background focus-visible:outline-ring"><X className="size-3" aria-hidden="true" /></button></Badge>)}
    </div>
  </div>
}
