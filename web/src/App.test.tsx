import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/routes'
import { ThemeProvider } from '@/components/ThemeProvider'
import { createQueryClient, queryKeys } from '@/lib/query'
import { advanceSession } from '@/lib/api'
import { zhCN } from '@/i18n/zh-CN'

const failure = (status: number, code: string, extra = {}) => Response.json({ error: { code, message: 'Request failed', hint: 'Try again', ...extra } }, { status })
const missingSetup = () => failure(404, 'not_found')
const admin = { username: 'admin' }
const status = { supported: false, cpu: 0, mem: { used: 0, total: 0 }, disk: { used: 0, total: 0 }, uptime: 0, net: { rx_rate: 0, tx_rate: 0 }, singbox: { installed: false, running: false, version: '', uptime: 0 } }
let signedIn = false
let setupResponse: () => Response = missingSetup
let loginResponse: () => Promise<Response>
let logoutResponse: () => Promise<Response>
let settingsResponse: () => Response
let sessionResponse: () => Response
const disposers: (() => void)[] = []

beforeEach(() => {
  advanceSession()
  signedIn = false
  setupResponse = missingSetup
  settingsResponse = () => Response.json({ panel_name: 'boxpanel' })
  sessionResponse = () => signedIn ? Response.json(admin) : failure(401, 'unauthorized')
  loginResponse = async () => { signedIn = true; return Response.json(admin) }
  logoutResponse = async () => { signedIn = false; return new Response(null, { status: 204 }) }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    switch (url.replace(/^.*\/api\/v1/, '')) {
      case '/auth/me': return sessionResponse()
      case '/auth/login': return loginResponse()
      case '/auth/logout': return logoutResponse()
      case '/setup/status': return setupResponse()
      case '/settings': return settingsResponse()
      case '/system/status': return Response.json(status)
      case '/users': case '/inbounds': return Response.json([])
      case '/users/3': return Response.json({ id: 3, name: 'alice', uuid: 'aaaa****bbbb', enabled: true, traffic_limit: 0, traffic_up: 0, traffic_down: 0, expire_at: 0, remark: '', created_at: 1, status: 'active' })
      case '/users/3/links': return failure(409, 'subscription_unavailable')
      default: throw new Error(`Unexpected request: ${url}`)
    }
  }))
})

afterEach(() => { cleanup(); disposers.splice(0).forEach((dispose) => dispose()) })

function mount(path = '/', basename = '/') {
  const client = createQueryClient()
  client.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 }, mutations: { retry: false } })
  const router = createMemoryRouter(routes, { basename, initialEntries: [path] })
  render(<QueryClientProvider client={client}><ThemeProvider><RouterProvider router={router} /></ThemeProvider></QueryClientProvider>)
  disposers.push(() => { router.dispose(); client.clear() })
  return { router, client, user: userEvent.setup() }
}

async function enterCredentials(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText(zhCN.login.username), 'admin')
  await user.type(screen.getByLabelText(zhCN.login.password, { exact: true }), 'secret')
}

describe('session and history routes', () => {
  it.each(['', '/k3m9x2p7q1ab'])('round-trips a deep link under %s', async (base) => {
    const { router, user } = mount(`${base}/users/3?view=links#share`, base || '/')
    await screen.findByRole('heading', { name: zhCN.login.title })
    expect(router.state.location.pathname).toBe(`${base}/login`)
    expect(new URLSearchParams(router.state.location.search).get('next')).toBe('/users/3?view=links#share')
    await enterCredentials(user)
    await user.click(screen.getByRole('button', { name: zhCN.login.submit }))
    await screen.findByRole('heading', { name: zhCN.pages.userDetail })
    expect(router.state.location.pathname).toBe(`${base}/users/3`)
    expect(router.state.location.search).toBe('?view=links')
    expect(router.state.location.hash).toBe('#share')
    expect(screen.getByRole('link', { name: zhCN.pages.users })).toHaveAttribute('href', `${base}/users`)
    expect(screen.getByRole('link', { name: zhCN.pages.users })).toHaveAttribute('aria-current', 'page')
  })

  it('rejects external next targets after login', async () => {
    signedIn = true
    const { router } = mount('/login?next=https%3A%2F%2Fevil.example')
    await screen.findByRole('heading', { name: zhCN.pages.home })
    expect(router.state.location.pathname).toBe('/')
  })

  it('redirects incomplete setup, including after login, without looping', async () => {
    setupResponse = () => Response.json({ completed: false, step: 'node' })
    const { router, user } = mount('/login?next=%2Fusers')
    await enterCredentials(user)
    await user.click(screen.getByRole('button', { name: zhCN.login.submit }))
    await screen.findByRole('heading', { name: zhCN.pages.setup })
    expect(router.state.location.pathname).toBe('/setup')
    await act(() => router.navigate('/setup/node'))
    expect(router.state.location.pathname).toBe('/setup/node')
  })

  it('permits completed setup and the explicit not-found compatibility response', async () => {
    signedIn = true
    setupResponse = () => Response.json({ completed: true, step: 'finish' })
    mount('/users')
    await screen.findByRole('heading', { name: zhCN.pages.users })
    expect(await screen.findByText(`${zhCN.status.label} · ${zhCN.status.unsupported}`)).toBeVisible()
  })

  it.each([500, 403, 404])('does not bypass a real setup error (%s), and can retry', async (code) => {
    signedIn = true
    setupResponse = () => failure(code, 'setup_failed')
    const { user } = mount('/users')
    await screen.findByRole('alert')
    expect(screen.queryByRole('heading', { name: zhCN.pages.users })).not.toBeInTheDocument()
    setupResponse = missingSetup
    await user.click(screen.getByRole('button', { name: zhCN.common.retry }))
    await screen.findByRole('heading', { name: zhCN.pages.users })
  })

  it('clears private cached data and returns to login on a protected 401', async () => {
    signedIn = true
    const { router, client } = mount('/users/3')
    await screen.findByRole('heading', { name: zhCN.pages.userDetail })
    client.setQueryData(['users', 3, 'links'], { secret: 'private' })
    settingsResponse = () => failure(401, 'unauthorized')
    signedIn = false
    await act(() => client.invalidateQueries({ queryKey: queryKeys.settings }))
    await screen.findByRole('heading', { name: zhCN.login.title })
    expect(client.getQueryData(['users', 3, 'links'])).toBeUndefined()
    expect(router.state.location.search).toBe('?next=%2Fusers%2F3')
  })

  it('does not mistake an unavailable session service for a logged-out session', async () => {
    sessionResponse = () => failure(500, 'internal_error')
    const { router, user } = mount('/users')
    await screen.findByRole('alert')
    expect(router.state.location.pathname).toBe('/users')
    expect(screen.queryByLabelText(zhCN.login.username)).not.toBeInTheDocument()
    sessionResponse = () => Response.json(admin)
    await user.click(screen.getByRole('button', { name: zhCN.common.retry }))
    await screen.findByRole('heading', { name: zhCN.pages.users })
  })

  it('blocks malformed setup responses instead of assuming completion', async () => {
    signedIn = true
    setupResponse = () => Response.json({ completed: 'false', step: 'welcome' })
    mount('/users')
    expect(await screen.findByText(zhCN.errors.response)).toBeVisible()
    expect(screen.queryByRole('heading', { name: zhCN.pages.users })).not.toBeInTheDocument()
  })

  it('renders a protected 404 and navigates home within the basename', async () => {
    signedIn = true
    const { router, user } = mount('/random/unknown/page', '/random')
    await screen.findByRole('heading', { name: zhCN.common.notFoundTitle })
    await user.click(screen.getByRole('link', { name: zhCN.common.backHome }))
    await screen.findByRole('heading', { name: zhCN.pages.home })
    expect(router.state.location.pathname).toBe('/random')
  })
})

describe('login and logout feedback', () => {
  it('validates locally, shows password accessibly, and never submits invalid data', async () => {
    const { user } = mount('/login')
    await user.click(await screen.findByRole('button', { name: zhCN.login.submit }))
    expect(await screen.findByText(zhCN.login.usernameInvalid)).toBeVisible()
    expect(screen.getByText(zhCN.login.passwordRequired)).toBeVisible()
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith('/auth/login'))).toBe(false)
    await user.click(screen.getByRole('button', { name: zhCN.login.showPassword }))
    expect(screen.getByLabelText(zhCN.login.password, { exact: true })).toHaveAttribute('type', 'text')
  })

  it('keeps invalid credentials inline and supports a successful retry', async () => {
    loginResponse = async () => failure(401, 'invalid_credentials', { detail: 'diagnostic' })
    const { router, user } = mount('/login?next=%2Fusers')
    await enterCredentials(user)
    await user.click(screen.getByRole('button', { name: zhCN.login.submit }))
    await screen.findByRole('alert')
    expect(router.state.location.search).toBe('?next=%2Fusers')
    expect(screen.getByText('Try again')).toBeVisible()
    expect(screen.getByText('diagnostic')).not.toBeVisible()
    await user.click(screen.getByText(zhCN.common.detail))
    expect(screen.getByText('diagnostic')).toBeVisible()
    loginResponse = async () => { signedIn = true; return Response.json(admin) }
    await user.click(screen.getByRole('button', { name: zhCN.login.submit }))
    await screen.findByRole('heading', { name: zhCN.pages.users })
  })

  it('disables submission while pending and shows rate-limit retry advice', async () => {
    let resolve!: (response: Response) => void
    loginResponse = () => new Promise((done) => { resolve = done })
    const { user } = mount('/login')
    await enterCredentials(user)
    await user.dblClick(screen.getByRole('button', { name: zhCN.login.submit }))
    expect(screen.getByRole('button', { name: zhCN.login.submitting })).toBeDisabled()
    expect(vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith('/auth/login'))).toHaveLength(1)
    const response = failure(429, 'rate_limited')
    response.headers.set('Retry-After', '900')
    await act(async () => resolve(response))
    expect(await screen.findByText(zhCN.login.retryAfter(900))).toBeVisible()
    expect(screen.getByRole('button', { name: zhCN.login.submit })).toBeEnabled()
  })

  it('retains session on failed logout, then clears all cached data on 204', async () => {
    signedIn = true
    logoutResponse = async () => failure(500, 'internal_error')
    const { client, user } = mount('/users')
    await screen.findByRole('heading', { name: zhCN.pages.users })
    client.setQueryData(['users', 'links'], { private: true })
    await user.click(screen.getByRole('button', { name: zhCN.app.logout }))
    await screen.findByRole('alert')
    expect(client.getQueryData(['users', 'links'])).toEqual({ private: true })
    let resolve!: (response: Response) => void
    logoutResponse = () => new Promise((done) => { resolve = done })
    await user.click(screen.getByRole('button', { name: zhCN.app.logout }))
    expect(screen.getByRole('button', { name: zhCN.app.loggingOut })).toBeDisabled()
    await act(async () => { signedIn = false; resolve(new Response(null, { status: 204 })) })
    await screen.findByRole('heading', { name: zhCN.login.title })
    expect(client.getQueryData(['users', 'links'])).toBeUndefined()
  })
})

it('opens mobile navigation with a named dialog, closes with Escape and on navigation', async () => {
  signedIn = true
  vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(375)
  const { user, router } = mount('/')
  await screen.findByRole('heading', { name: zhCN.pages.home })
  const trigger = screen.getByRole('button', { name: zhCN.app.openNavigation })
  await user.click(trigger)
  const dialog = screen.getByRole('dialog', { name: zhCN.app.navigation })
  expect(dialog).toBeVisible()
  await user.keyboard('{Escape}')
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  expect(trigger).toHaveFocus()
  await user.click(trigger)
  await user.click(within(screen.getByRole('dialog')).getByRole('link', { name: zhCN.pages.users }))
  await screen.findByRole('heading', { name: zhCN.pages.users })
  expect(router.state.location.pathname).toBe('/users')
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})
