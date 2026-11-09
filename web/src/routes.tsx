import type { RouteObject } from 'react-router'
import App from '@/App'
import { SessionGuard } from '@/components/SessionGuard'
import { NotFoundPage, RouteErrorPage } from '@/pages/not-found'
import { PlaceholderPage } from '@/pages/placeholders'
import { zhCN } from '@/i18n/zh-CN'
import { AppLayout, LoginPage } from '@/pages/lazy'

export const routes: RouteObject[] = [{
  Component: App,
  ErrorBoundary: RouteErrorPage,
  children: [
    { path: '/login', Component: LoginPage },
    { Component: SessionGuard, children: [{ Component: AppLayout, children: [
      { index: true, element: <PlaceholderPage title={zhCN.pages.home} /> },
      { path: 'inbounds', element: <PlaceholderPage title={zhCN.pages.inbounds} /> },
      { path: 'inbounds/new', element: <PlaceholderPage title={zhCN.pages.newInbound} /> },
      { path: 'inbounds/:id', element: <PlaceholderPage title={zhCN.pages.editInbound} /> },
      { path: 'users', element: <PlaceholderPage title={zhCN.pages.users} /> },
      { path: 'users/:id', element: <PlaceholderPage title={zhCN.pages.userDetail} /> },
      { path: 'rules', element: <PlaceholderPage title={zhCN.pages.rules} /> },
      { path: 'settings', element: <PlaceholderPage title={zhCN.pages.settings} /> },
      { path: 'logs', element: <PlaceholderPage title={zhCN.pages.logs} /> },
      { path: 'setup', element: <PlaceholderPage title={zhCN.pages.setup} setup /> },
      { path: 'setup/:step', element: <PlaceholderPage title={zhCN.pages.setup} setup /> },
      { path: '*', Component: NotFoundPage },
    ] }] },
  ],
}]
