import type { ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

export function PageTabs({ label, tabs }: {
  label: string
  tabs: readonly { id: string; label: string; content: ReactNode }[]
}) {
  const [params, setParams] = useSearchParams()
  const active = tabs.find((tab) => tab.id === params.get('tab')) ?? tabs[0]
  if (!active) return null
  return <Tabs value={active.id} onValueChange={(value) => {
    if (!tabs.some((tab) => tab.id === value)) return
    const next = new URLSearchParams(params)
    if (value === tabs[0]?.id) next.delete('tab')
    else next.set('tab', String(value))
    setParams(next)
  }} className="min-w-0 gap-5">
    <TabsList aria-label={label} className="w-full flex-wrap justify-start gap-1 group-data-horizontal/tabs:h-auto sm:w-fit">
      {tabs.map((tab) => <TabsTrigger key={tab.id} value={tab.id} className="min-h-10 min-w-fit flex-1 px-3 sm:min-h-8 sm:flex-none">{tab.label}</TabsTrigger>)}
    </TabsList>
    <TabsContent key={active.id} value={active.id} className="min-w-0 space-y-4">{active.content}</TabsContent>
  </Tabs>
}
