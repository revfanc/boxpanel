import type { RouteObject } from 'react-router'
import App from '@/App'
import { SessionGuard } from '@/components/SessionGuard'
import { NotFoundPage, RouteErrorPage } from '@/pages/not-found'
import { AppLayout, LoginPage, DashboardPage, LogsPage, SettingsPage, InboundsPage, NewInboundPage, EditInboundPage, UsersPage, UserDetailPage, RulesPage, SetupPage } from '@/pages/lazy'

export const routes: RouteObject[] = [{
  Component: App,
  ErrorBoundary: RouteErrorPage,
  children: [
    { path: '/login', Component: LoginPage },
    { Component: SessionGuard, children: [
      { path: 'setup', Component: SetupPage },
      { path: 'setup/:step', Component: SetupPage },
      { Component: AppLayout, children: [
      { index: true, Component: DashboardPage },
      { path: 'inbounds', Component: InboundsPage },
      { path: 'inbounds/new', Component: NewInboundPage },
      { path: 'inbounds/:id', Component: EditInboundPage },
      { path: 'users', Component: UsersPage },
      { path: 'users/:id', Component: UserDetailPage },
      { path: 'rules', Component: RulesPage },
      { path: 'settings', Component: SettingsPage },
      { path: 'logs', Component: LogsPage },
      { path: '*', Component: NotFoundPage },
    ] }] },
  ],
}]
