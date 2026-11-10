import { Suspense, useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Outlet, useLocation, useNavigate } from 'react-router'
import { onUnauthorized } from '@/lib/api'
import { loginPath } from '@/lib/navigation'
import { replaceSession } from '@/lib/query'
import { PanelAddressProvider } from '@/components/PanelAddressDialog'
import { Loading } from '@/components/Feedback'

export default function App() {
  const client = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  useEffect(() => onUnauthorized(() => {
    replaceSession(client, null)
    if (location.pathname !== '/login') {
      void navigate(loginPath(`${location.pathname}${location.search}${location.hash}`), { replace: true })
    }
  }), [client, navigate, location])
  return <PanelAddressProvider><Suspense fallback={<Loading />}><Outlet /></Suspense></PanelAddressProvider>
}
