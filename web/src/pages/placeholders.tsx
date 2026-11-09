import { Link, useParams } from 'react-router'
import { zhCN } from '@/i18n/zh-CN'

export function Home() {
  return (
    <section className="space-y-3">
      <h2 className="text-lg font-semibold">{zhCN.pages.home}</h2>
      <p className="text-sm text-muted-foreground">{zhCN.app.placeholder}</p>
    </section>
  )
}

export function Users() {
  return (
    <section className="space-y-3">
      <h2 className="text-lg font-semibold">{zhCN.pages.users}</h2>
      <p className="text-sm text-muted-foreground">{zhCN.app.placeholder}</p>
      <Link className="text-sm underline underline-offset-4" to="/users/3">
        {zhCN.pages.exampleUser}
      </Link>
    </section>
  )
}

export function UserDetail() {
  const { id } = useParams()

  return (
    <section className="space-y-3">
      <h2 className="text-lg font-semibold">{zhCN.pages.userDetail}</h2>
      <p className="text-sm">{zhCN.pages.userID}: <span>{id}</span></p>
      <p className="text-sm text-muted-foreground">{zhCN.app.placeholder}</p>
    </section>
  )
}
