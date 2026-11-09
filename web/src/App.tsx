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
        <CardContent>
          <p className="text-sm text-muted-foreground">{zhCN.app.placeholder}</p>
        </CardContent>
      </Card>
    </main>
  )
}
