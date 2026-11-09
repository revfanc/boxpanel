import { useQuery } from '@tanstack/react-query'
import { Navigate, Outlet, useLocation } from 'react-router'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { loginPath } from '@/lib/navigation'
import { sessionQuery, setupQuery } from '@/lib/query'
import { zhCN } from '@/i18n/zh-CN'

export function SessionGuard() {
  const session = useQuery(sessionQuery)
  const location = useLocation()
  if (session.isPending) return <Loading label={zhCN.common.checkingSession} />
  if (session.isError) return <div className="mx-auto w-full max-w-lg p-4"><ErrorNotice error={session.error} retry={() => { void session.refetch() }} /></div>
  if (!session.data) return <Navigate replace to={loginPath(`${location.pathname}${location.search}${location.hash}`)} />
  return <SetupGuard />
}

function SetupGuard() {
  const setup = useQuery(setupQuery)
  const location = useLocation()
  if (setup.isPending) return <Loading label={zhCN.common.checkingSetup} />
  if (setup.isError) return <div className="mx-auto w-full max-w-lg p-4"><ErrorNotice error={setup.error} retry={() => { void setup.refetch() }} /></div>
  if (setup.data?.completed === false && !/^\/setup(?:\/|$)/.test(location.pathname)) return <Navigate replace to="/setup" />
  return <Outlet />
}
