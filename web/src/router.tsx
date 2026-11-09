import { createBrowserRouter } from 'react-router'
import { BASE } from '@/lib/runtime'
import { routes } from '@/routes'

export const router = createBrowserRouter(routes, { basename: BASE || '/' })
