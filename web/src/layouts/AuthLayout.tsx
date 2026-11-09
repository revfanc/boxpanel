import type { ReactNode } from 'react'
import { Box } from 'lucide-react'
import { ThemeSwitch } from '@/components/ThemeSwitch'
import { zhCN } from '@/i18n/zh-CN'

export function AuthLayout({ children }: { children: ReactNode }) {
  return <div className="flex min-h-svh flex-col bg-muted/30">
    <header className="flex items-center justify-between gap-4 px-5 py-4 sm:px-8">
      <div className="flex items-center gap-2.5 font-semibold tracking-tight"><Box className="size-6 text-primary" aria-hidden="true" />{zhCN.app.name}</div>
      <ThemeSwitch />
    </header>
    <main className="flex flex-1 items-center justify-center px-4 py-8 sm:py-16">
      <div className="w-full min-w-0 max-w-sm">{children}</div>
    </main>
    <footer className="px-4 py-5 text-center text-xs text-muted-foreground">{zhCN.app.description}</footer>
  </div>
}
