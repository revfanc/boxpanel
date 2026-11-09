import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { zhCN } from '@/i18n/zh-CN'

export function NotFoundPage() {
  return <section className="mx-auto flex min-h-80 max-w-md flex-col items-center justify-center gap-4 py-10 text-center">
    <span className="text-6xl font-semibold tracking-tight text-muted-foreground/50" aria-hidden="true">404</span>
    <h1 className="text-xl font-semibold">{zhCN.common.notFoundTitle}</h1>
    <p className="text-sm leading-7 text-muted-foreground">{zhCN.common.notFoundHint}</p>
    <Button asChild><Link to="/">{zhCN.common.backHome}</Link></Button>
  </section>
}

export function RouteErrorPage() {
  return <main className="mx-auto max-w-lg space-y-4 p-6 text-center">
    <h1 className="text-xl font-semibold">{zhCN.common.pageError}</h1>
    <p className="text-sm text-muted-foreground">{zhCN.common.pageErrorHint}</p>
    <Button asChild><Link to="/">{zhCN.common.backHome}</Link></Button>
  </main>
}
