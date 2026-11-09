import type { ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { ThemeSwitch } from '@/components/ThemeSwitch'
import { setupSteps, type SetupStep } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export function SetupLayout({ step, busy, skip, back, children }: {
  step: SetupStep; busy: boolean; skip: () => void; back: () => void; children: ReactNode
}) {
  const index = setupSteps.indexOf(step)
  return <main className="mx-auto min-h-svh w-full max-w-3xl space-y-6 p-4 pb-12 [overflow-wrap:anywhere] sm:p-8">
    <header className="flex flex-wrap items-center justify-between gap-3"><h1 className="text-xl font-semibold">{t.pages.setup}</h1><div className="flex items-center gap-2"><ThemeSwitch /><Button variant="ghost" disabled={busy} onClick={skip}>{t.setup.skip}</Button></div></header>
    <div className="space-y-3"><Progress value={(index + 1) / 7 * 100} aria-label={t.setup.progress} aria-valuetext={t.setup.position(index + 1)} />
      <ol className="grid grid-cols-4 gap-2 text-xs sm:grid-cols-7">{setupSteps.map((item, i) => <li key={item} aria-current={step === item ? 'step' : undefined} className={item === step ? 'font-semibold text-primary' : 'text-muted-foreground'}>{i + 1}. {t.setup.steps[item]}</li>)}</ol>
    </div>
    <div className="flex items-center justify-between gap-3"><h2 className="text-2xl font-semibold">{t.setup.steps[step]}</h2><Button variant="outline" disabled={busy || index === 0} onClick={back}>{t.setup.back}</Button></div>
    {children}
  </main>
}
