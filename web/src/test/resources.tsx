import { PanelAddressProvider } from '@/components/PanelAddressDialog'
import type { ReactNode } from 'react'
import { render, cleanup } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, vi } from 'vitest'
import { createQueryClient } from '@/lib/query'
import type { PanelSettings, SystemStatus, Inbound, User, RulesSettings, UserLinks, CertificateStatus, AlertOverview, AlertSettings } from '@/types/api'

export const settings: PanelSettings = { panel_name: 'boxpanel', public_host: 'example.com', web_port: 2053, web_path: '', sub_path: 'subscription', panel_tls_mode: 'http', block_bittorrent: true }
export const certificate: CertificateStatus = { mode: 'http', type: 'http', host: '', issuer: '', not_before: 0, not_after: 0, last_attempt: 0, last_success: 0, last_error: '', hint: '', failures: 0, next_attempt: 0, issuing: false, warning: false }
export const status: SystemStatus = { supported: true, cpu: 12.5, mem: { used: 1024, total: 2048 }, disk: { used: 1024, total: 4096 }, uptime: 3600, net: { rx_rate: 512, tx_rate: 1024 }, singbox: { installed: true, running: true, version: '1.14.4', uptime: 60 } }
export const alertOverview: AlertOverview = {
  billing: { supported: false, interface: '', sampled_at: 0, stale: false, starts_at: 1735689600, ends_at: 1738368000, billing_mode: 'out', used: 0, quota: 0, percent: 0 },
  unread: [], unread_count: 0, recent: [],
}
export const alertSettings: AlertSettings = {
  config: {
    interface: '', quota_gb: 0, billing_day: 1, billing_mode: 'out',
    events: {
      user_quota_80: true, user_quota_100: true, user_expiring: true, user_expired: true, user_stopped: true,
      server_quota_80: true, server_quota_90: true, server_quota_100: true,
      certificate_warning: true, core_down: true, core_recovered: true,
    },
  },
  channels: {
    bark: { enabled: false, url: 'https://api.day.app', credential_mask: '' },
    server_chan: { enabled: false, credential_mask: '' },
    telegram: { enabled: false, credential_mask: '' },
  },
  interfaces: [],
}
export const inbound: Inbound = { id: 1, tag: 'reality-443', protocol: 'vless_reality', port: 443, enabled: true, remark: 'Reality', options: { server_name: 'example.com', handshake_server: 'example.com', handshake_port: 443, fingerprint: 'chrome', short_ids: ['0123456789abcdef'], public_key: 'public-key' }, created_at: 1, updated_at: 1 }
export const proxyUser: User = { id: 1, name: 'alice', uuid: 'aaaa****bbbb', enabled: true, traffic_limit: 1073741824, traffic_reset_day: 0, traffic_reset_at: 0, traffic_up: 1024, traffic_down: 1024, expire_at: 0, remark: '', created_at: 1, status: 'active' }
export const rules: RulesSettings = { preset: 'bypass_cn', block_ads: false, custom_direct: [], custom_proxy: [], source: 'metacubex', mirror: '', update_interval: '1d', dns: { remote: 'https://1.1.1.1/dns-query', local: 'https://223.5.5.5/dns-query', fakeip: false }, ipv6: false }
export const links: UserLinks = { subscription: { auto: 'https://example.com/sub/token', singbox: 'https://example.com/sub/token?format=singbox', clash: 'https://example.com/sub/token?format=clash', base64: 'https://example.com/sub/token?format=base64' }, import: { singbox: 'sing-box://import-remote-profile?url=test', clash: 'clash://install-config?url=test' }, nodes: [{ inbound_id: 1, remark: 'Reality', uri: 'vless://credential@example.com:443' }] }
export const failure = (status = 500, code = 'internal_error') => Response.json({ error: { code, message: 'Request failed', hint: 'Try again', detail: 'Diagnostic detail' } }, { status })
export function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
type Responder = (init: RequestInit) => Response | Promise<Response>
export function mockAPI(overrides: Record<string, Responder> = {}) {
  const defaults: Record<string, unknown> = { '/alerts': alertOverview, '/alerts/settings': alertSettings, '/backup/status': { last_attempt: 0, last_success: 0, result: '' }, '/cert': certificate, '/settings': settings, '/auth/me': { username: 'admin' }, '/system/status': status, '/inbounds': [], '/users': [], '/inbounds/1': inbound, '/users/1': proxyUser, '/users/1/links': links, '/rules': rules, '/singbox/logs?lines=200': { lines: [] } }
  const fetch = vi.fn(async (url: string, init: RequestInit = {}) => {
    const path = url.replace(/^.*\/api\/v1/, '')
    const respond = overrides[`${init.method ?? 'GET'} ${path}`] ?? overrides[path]
    if (respond) return respond(init)
    if ((init.method ?? 'GET') === 'GET' && path in defaults) return Response.json(defaults[path])
    throw new Error(`Unexpected request ${init.method} ${path}`)
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}
const disposers: (() => void)[] = []
afterEach(() => { cleanup(); disposers.splice(0).forEach((dispose) => dispose()) })
export function mountPage(element: ReactNode, path = '/', pattern = path) {
  const client = createQueryClient()
  client.setDefaultOptions({ queries: { retry: false, staleTime: 30000 }, mutations: { retry: false } })
  const router = createMemoryRouter([{ path: pattern, element }, { path: '*', element: <div data-testid="destination" /> }], { initialEntries: [path] })
  render(<QueryClientProvider client={client}><PanelAddressProvider><RouterProvider router={router} /></PanelAddressProvider></QueryClientProvider>)
  disposers.push(() => { router.dispose(); client.clear() })
  return { client, router, user: userEvent.setup() }
}
