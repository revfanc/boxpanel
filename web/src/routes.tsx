import type { RouteObject } from 'react-router'
import App from '@/App'
import { SessionGuard } from '@/components/SessionGuard'
import { NotFoundPage, RouteErrorPage } from '@/pages/not-found'
import { PlaceholderPage } from '@/pages/placeholders'
import { zhCN } from '@/i18n/zh-CN'
import { AppLayout, LoginPage, DashboardPage, LogsPage, SettingsPage, InboundsPage, NewInboundPage, EditInboundPage, UsersPage, UserDetailPage, RulesPage } from '@/pages/lazy'

export const routes: RouteObject[] = [{
  Component: App,
  ErrorBoundary: RouteErrorPage,
  children: [
    { path: '/login', Component: LoginPage },
    { Component: SessionGuard, children: [{ Component: AppLayout, children: [
      { index: true, Component: DashboardPage },
      { path: 'inbounds', Component: InboundsPage },
      { path: 'inbounds/new', Component: NewInboundPage },
      { path: 'inbounds/:id', Component: EditInboundPage },
      { path: 'users', Component: UsersPage },
      { path: 'users/:id', Component: UserDetailPage },
      { path: 'rules', Component: RulesPage },
      { path: 'settings', Component: SettingsPage },
      { path: 'logs', Component: LogsPage },
      { path: 'setup', element: <PlaceholderPage title={zhCN.pages.setup} setup /> },
      { path: 'setup/:step', element: <PlaceholderPage title={zhCN.pages.setup} setup /> },
      { path: '*', Component: NotFoundPage },
    ] }] },
  ],
}]
