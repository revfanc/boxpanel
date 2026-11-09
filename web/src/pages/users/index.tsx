import { Link } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Page, Panel, Empty } from '@/components/ResourceUI'
import { Loading, ErrorNotice } from '@/components/Feedback'
import { usersQuery } from '@/lib/resources'
import { UserEditor, UserUsage, UserActions } from './components'
import { zhCN as t } from '@/i18n/zh-CN'

export function UsersPage() {
  const users = useQuery(usersQuery)
  return <Page title={t.pages.users} description={t.users.description} actions={<UserEditor />}>
    {users.isPending ? <Loading /> : users.isError ? <ErrorNotice error={users.error} retry={() => { void users.refetch() }} /> : users.data.length === 0 ? <Empty>{t.users.empty}</Empty> : <div className="grid min-w-0 gap-4 lg:grid-cols-2">
      {users.data.map((user) => <Panel key={user.id} title={user.name} hint={user.remark}>
        <UserUsage user={user} />
        <Button variant="outline" asChild><Link to={`/users/${user.id}`}>{t.pages.userDetail}</Link></Button>
        <UserActions user={user} />
      </Panel>)}
    </div>}
  </Page>
}
