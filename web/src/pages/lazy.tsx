import { lazy } from 'react'

export const LoginPage = lazy(() => import('@/pages/login').then((page) => ({ default: page.LoginPage })))
export const AppLayout = lazy(() => import('@/layouts/AppLayout').then((layout) => ({ default: layout.AppLayout })))
export const DashboardPage = lazy(() => import('@/pages/dashboard').then((page) => ({ default: page.DashboardPage })))
export const LogsPage = lazy(() => import('@/pages/logs').then((page) => ({ default: page.LogsPage })))
export const SettingsPage = lazy(() => import('@/pages/settings').then((page) => ({ default: page.SettingsPage })))
export const InboundsPage = lazy(() => import('@/pages/inbounds').then((page) => ({ default: page.InboundsPage })))
export const NewInboundPage = lazy(() => import('@/pages/inbounds/editor').then((page) => ({ default: page.NewInboundPage })))
export const EditInboundPage = lazy(() => import('@/pages/inbounds/editor').then((page) => ({ default: page.EditInboundPage })))
export const UsersPage = lazy(() => import('@/pages/users').then((page) => ({ default: page.UsersPage })))
export const UserDetailPage = lazy(() => import('@/pages/users/detail').then((page) => ({ default: page.UserDetailPage })))
export const RulesPage = lazy(() => import('@/pages/rules').then((page) => ({ default: page.RulesPage })))

export const SetupPage = lazy(() => import('@/pages/setup').then((page) => ({ default: page.SetupPage })))
