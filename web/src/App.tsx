import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/card'
import { zhCN } from '@/i18n/zh-CN'

export default function App() {
  return (
    <main className="flex min-h-svh items-center justify-center bg-background p-6 text-foreground">
      <Card className="w-full max-w-md">
        <CardHeader>
          <h1 className="text-2xl font-semibold">{zhCN.app.name}</h1>
          <CardDescription>{zhCN.app.description}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          <nav aria-label={zhCN.app.navigation} className="flex gap-4 text-sm">
            <Link className="underline underline-offset-4" to="/">{zhCN.pages.home}</Link>
            <Link className="underline underline-offset-4" to="/users">{zhCN.pages.users}</Link>
          </nav>
          <Outlet />
        </CardContent>
      </Card>
    </main>
  )
}
import { Link, Outlet } from 'react-router'
