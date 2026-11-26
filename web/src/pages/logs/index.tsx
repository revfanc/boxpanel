import { Navigate } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { NotFoundPage } from '@/pages/not-found'
import { serverPath } from '@/lib/navigation'
import { serversQuery } from '@/lib/resources'

export function LogsPage() {
  const query = useQuery(serversQuery)
  if (query.isPending) return <Loading />
  if (query.isError) return <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
  const local = query.data.find((server) => server.is_local)
  return local ? <Navigate to={serverPath(local.id, 'logs')} replace /> : <NotFoundPage />
}
