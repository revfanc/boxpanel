import type { RouteObject } from 'react-router'
import App from '@/App'
import { Home, UserDetail, Users } from '@/pages/placeholders'

export const routes: RouteObject[] = [
  {
    path: '/',
    Component: App,
    children: [
      { index: true, Component: Home },
      { path: 'users', Component: Users },
      { path: 'users/:id', Component: UserDetail },
    ],
  },
]
