import { lazy } from 'react'

export const LoginPage = lazy(() => import('@/pages/login').then((page) => ({ default: page.LoginPage })))
export const AppLayout = lazy(() => import('@/layouts/AppLayout').then((layout) => ({ default: layout.AppLayout })))
