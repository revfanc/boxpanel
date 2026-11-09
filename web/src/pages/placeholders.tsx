import { Construction } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { zhCN } from '@/i18n/zh-CN'

export function PlaceholderPage({ title, setup = false }: { title: string; setup?: boolean }) {
  return <section className="space-y-6">
    <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
    <Card className="border-dashed bg-muted/20 shadow-none">
      <CardContent className="flex min-h-64 flex-col items-center justify-center px-5 py-10 text-center sm:min-h-80">
        <div className="mb-5 rounded-xl border bg-background p-3"><Construction className="size-6 text-muted-foreground" aria-hidden="true" /></div>
        <h2 className="font-medium">{zhCN.common.placeholderTitle}</h2>
        <p className="mt-2 max-w-md text-sm leading-7 text-muted-foreground">{setup ? zhCN.common.setupHint : zhCN.common.placeholderHint}</p>
      </CardContent>
    </Card>
  </section>
}
