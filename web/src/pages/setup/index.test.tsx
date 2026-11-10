import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, expect, it } from 'vitest'
import { routes } from '@/routes'
import { ThemeProvider } from '@/components/ThemeProvider'
import { createQueryClient } from '@/lib/query'
import { advanceSession } from '@/lib/api'
import { mockAPI, failure, deferred, inbound, proxyUser, links, settings, status as systemStatus } from '@/test/resources'
import { setupSteps, type SetupStatus, type SystemCheck, type Inbound, type User } from '@/types/api'
import { clients, platforms } from '@/data/clients'
import { zhCN as t, subscriptionText as sub } from '@/i18n/zh-CN'

const check: SystemCheck = { os: '', arch: 'arm64', virt: '', ipv4: '', ipv6: '', bbr: { enabled: false, available: false }, firewall: { type: 'none', active: false }, singbox: systemStatus.singbox, ports: [], items: [{ id: 'platform', level: 'warn', title: 'Development', hint: 'Use Linux to apply' }] }
const hy2: Inbound = { ...inbound, id: 2, tag: 'hysteria2-8443', protocol: 'hysteria2', port: 8443, remark: 'Hysteria2', options: { sni: 'www.bing.com', cert_mode: 'self_signed', up_mbps: 0, down_mbps: 0, port_hopping: { enabled: false, start: 20000, end: 30000 } } }
const baseStatus: SetupStatus = { completed: false, step: 'welcome', domain: '', hysteria2: true }
const disposers: (() => void)[] = []
afterEach(() => { cleanup(); disposers.splice(0).forEach((dispose) => dispose()) })
function mount(path = '/setup') {
  advanceSession()
  const client = createQueryClient()
  client.setDefaultOptions({ queries: { retry: false, staleTime: 30000 }, mutations: { retry: false } })
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  const view = render(<QueryClientProvider client={client}><ThemeProvider><RouterProvider router={router} /></ThemeProvider></QueryClientProvider>)
  disposers.push(() => { router.dispose(); client.clear() })
  return { ...view, client, router, user: userEvent.setup() }
}
function fixture(initial: Partial<SetupStatus> = {}, overrides: Parameters<typeof mockAPI>[0] = {}) {
  let state = { ...baseStatus, ...initial }
  const fetch = mockAPI({
    '/setup/status': (init) => { if (init?.method === 'PUT') state = { ...state, ...JSON.parse(String(init?.body)) }; return Response.json(state) },
    '/system/check': () => Response.json(check),
    '/inbounds': () => Response.json([inbound, hy2]),
    '/users': () => Response.json([proxyUser]),
    ...overrides,
  })
  return { fetch, get status() { return state } }
}
async function step(name: SetupStatus['step']) { await screen.findByRole('heading', { name: t.setup.steps[name], level: 2 }) }

it('forces a fresh login into setup instead of the requested protected page', async () => {
  let loggedIn = false
  fixture({}, { '/auth/me': () => loggedIn ? Response.json({ username: 'admin' }) : failure(401, 'unauthorized'), '/auth/login': () => { loggedIn = true; return Response.json({ username: 'admin' }) } })
  const { user, router } = mount('/users')
  await user.type(await screen.findByLabelText(t.login.username), 'admin')
  await user.type(screen.getByLabelText(t.login.password, { exact: true }), 'password')
  await user.click(screen.getByRole('button', { name: t.login.submit }))
  await step('welcome')
  expect(router.state.location.pathname).toBe('/setup/welcome')
  expect(screen.getByRole('button', { name: t.setup.back })).toBeDisabled()
})

it('persists skip, reports failed writes, and does not create resources', async () => {
  let fail = true
  let saved = false
  const f = fixture({ step: 'scenario' }, { 'PUT /setup/status': (init) => { if (fail) return failure(); saved = JSON.parse(String(init?.body)).completed; return Response.json({ ...baseStatus, step: 'scenario', completed: saved }) } })
  const { user, router } = mount('/users')
  await step('scenario')
  await user.click(screen.getByRole('button', { name: t.setup.skip }))
  expect(await screen.findByText('Try again')).toBeVisible()
  expect(router.state.location.pathname).toBe('/setup/scenario')
  fail = false
  await user.click(screen.getByRole('button', { name: t.setup.skip }))
  await screen.findByRole('heading', { name: t.pages.home })
  expect(saved).toBe(true)
  expect(f.fetch.mock.calls.some(([url, init]) => /\/(users|inbounds)$/.test(url) && init?.method === 'POST')).toBe(false)
})

it('resumes server progress and scenario choices after a new mount with an empty cache', async () => {
  const f = fixture({ step: 'scenario' })
  const first = mount('/setup/welcome')
  await step('scenario')
  await first.user.click(screen.getByRole('checkbox', { name: t.setup.hy2 }))
  await first.user.click(screen.getByRole('button', { name: t.setup.next }))
  await step('node')
  expect(f.status).toMatchObject({ step: 'node', hysteria2: false })
  first.unmount(); first.router.dispose(); first.client.clear()
  const second = mount('/setup')
  await step('node')
  await second.user.click(screen.getByRole('button', { name: t.setup.back }))
  await step('scenario')
  expect(screen.getByRole('checkbox', { name: t.setup.hy2 })).not.toBeChecked()
  expect(f.status.step).toBe('scenario')
})

it.each(setupSteps.slice(1))('persists Back from %s', async (current) => {
  const f = fixture({ step: current })
  const { user, router } = mount(`/setup/${current}`)
  await step(current)
  await user.click(screen.getByRole('button', { name: t.setup.back }))
  const previous = setupSteps[setupSteps.indexOf(current) - 1]!
  await step(previous)
  expect(f.status.step).toBe(previous)
  expect(router.state.location.pathname).toBe(`/setup/${previous}`)
})

it.each([
  { ipv4: '203.0.113.8', ipv6: '2001:db8::8' },
  { ipv4: '203.0.113.8', ipv6: '' },
  { ipv4: '', ipv6: '2001:db8::8' },
  { ipv4: '', ipv6: '' },
])('shows discovered address families without inventing missing ones: %j', async (ips) => {
  fixture({ step: 'check' }, { '/system/check': () => Response.json({ ...check, ...ips }) })
  mount()
  await screen.findByText('Use Linux to apply')
  const addresses = screen.queryAllByRole('definition').map((node) => node.textContent)
  expect(addresses).toEqual([ips.ipv4, ips.ipv6].filter(Boolean))
  if (ips.ipv4 || ips.ipv6) expect(screen.getByText(t.setup.publicIPsHint)).toBeVisible()
  else expect(screen.queryByText(t.setup.publicIPs)).not.toBeInTheDocument()
})

it('blocks check errors, enables BBR and rechecks before proceeding', async () => {
  let fixed = false
  let bbr = false
  fixture({ step: 'check' }, {
    '/system/check': () => Response.json({ ...check, bbr: { enabled: bbr, available: true }, items: [...(!fixed ? [{ id: 'ip', level: 'error', title: 'No IP', hint: 'Repair network' }] : []), ...(!bbr ? [{ id: 'bbr', level: 'warn', title: 'BBR', hint: 'Enable BBR', action: 'enable_bbr' }] : [])] }),
    '/system/bbr': () => { bbr = true; return Response.json({ enabled: true, available: true }) },
  })
  const { user } = mount()
  await screen.findByText('Repair network')
  expect(screen.getByRole('button', { name: t.setup.next })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: t.setup.bbr }))
  await waitFor(() => expect(screen.queryByRole('button', { name: t.setup.bbr })).not.toBeInTheDocument())
  fixed = true
  await user.click(screen.getByRole('button', { name: t.common.refresh }))
  await waitFor(() => expect(screen.getByRole('button', { name: t.setup.next })).toBeEnabled())
  await user.click(screen.getByRole('button', { name: t.setup.next }))
  await step('scenario')
})

it('validates domain input and stays on scenario when server DNS validation fails', async () => {
  let matches = false
  let body: Partial<SetupStatus> = {}
  fixture({ step: 'scenario' }, { 'PUT /setup/status': (init) => { body = JSON.parse(String(init?.body)); return matches ? Response.json({ ...baseStatus, ...body }) : failure(409, 'domain_not_pointing_here') } })
  const { user } = mount()
  await step('scenario')
  await user.click(screen.getByRole('radio', { name: new RegExp(t.setup.hasDomain) }))
  await user.click(screen.getByRole('button', { name: t.setup.next }))
  expect(await screen.findByText(t.setup.invalidDomain)).toBeVisible()
  expect(body).toEqual({})
  await user.type(screen.getByLabelText(t.setup.domain), 'node.example.com')
  await user.click(screen.getByRole('button', { name: t.setup.next }))
  expect(await screen.findByText('Try again')).toBeVisible()
  expect(body).toMatchObject({ domain: 'node.example.com', step: 'node', hysteria2: true })
  matches = true
  await user.click(screen.getByRole('button', { name: t.setup.next }))
  await step('node')
})

it('walks all seven steps using shared forms, manual Reality fallback, and dev subscription recovery', async () => {
  const nodes: Inbound[] = []
  const users: User[] = []
  let hy2Fail = true
  const f = fixture({}, {
    'GET /inbounds': () => Response.json(nodes), 'GET /users': () => Response.json(users),
    '/inbounds/suggest': (init) => JSON.parse(String(init?.body)).protocol === 'vless_reality' ? failure(409, 'reality_target_unavailable') : Response.json({ protocol: 'hysteria2', port: 8443, options: hy2.options }),
    '/reality/recommend': () => Response.json({ targets: [] }),
    'POST /inbounds': (init) => { const body = JSON.parse(String(init?.body)); if (body.protocol === 'hysteria2' && hy2Fail) return failure(422, 'singbox_check_failed'); const node = body.protocol === 'vless_reality' ? { ...inbound, ...body } : { ...hy2, ...body }; nodes.push(node); return Response.json(node) },
    'POST /users': (init) => { const saved = { ...proxyUser, ...JSON.parse(String(init?.body)) }; users.push(saved); return Response.json(saved) },
    '/system/port-probe': () => failure(409, 'system_unsupported'),
    '/users/1/links': () => failure(409, 'subscription_unavailable'),
  })
  const { user } = mount()
  await step('welcome'); await user.click(screen.getByRole('button', { name: t.setup.start }))
  await step('check'); await user.click(await screen.findByRole('button', { name: t.setup.next }))
  await step('scenario'); expect(screen.getByRole('checkbox', { name: t.setup.hy2 })).toBeChecked()
  await user.click(screen.getByRole('button', { name: t.setup.next }))
  await step('node')
  await user.type(await screen.findByLabelText(t.inbounds.target), 'www.example.com')
  await user.click(screen.getByRole('button', { name: t.setup.createNext }))
  await screen.findByLabelText(t.inbounds.sni)
  await user.click(screen.getByRole('button', { name: t.setup.createNext }))
  expect(await screen.findByText('Try again')).toBeVisible()
  expect(nodes).toHaveLength(1)
  hy2Fail = false
  await user.click(screen.getByRole('button', { name: t.setup.createNext }))
  await step('user')
  expect(await screen.findByLabelText(t.users.name)).toHaveValue('me')
  await user.click(screen.getByRole('button', { name: t.setup.createNext }))
  await step('firewall')
  expect(await screen.findByText('443/TCP')).toBeVisible()
  expect(screen.getByText('8443/UDP')).toBeVisible()
  expect(screen.getByText('2053/TCP')).toBeVisible()
  expect(screen.getByText(t.setup.firewallDev)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.setup.probe }))
  expect(await screen.findByText('Try again')).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.setup.next }))
  await step('finish')
  expect(await screen.findByText(t.setup.unavailable)).toBeVisible()
  expect(screen.queryByRole('img', { name: sub.qr })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: t.setup.import })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: t.setup.enter }))
  await screen.findByRole('heading', { name: t.pages.home })
  expect(f.status).toMatchObject({ completed: true, step: 'finish' })
  expect(nodes).toHaveLength(2); expect(users).toHaveLength(1)
  const realityWrites = f.fetch.mock.calls.filter(([url, init]) => url.endsWith('/inbounds') && init?.method === 'POST' && JSON.parse(String(init?.body)).protocol === 'vless_reality')
  expect(realityWrites).toHaveLength(1)
})

it('disables Back and skip during resource creation and reuses nodes after a progress write failure', async () => {
  const pending = deferred<Response>()
  const nodes: Inbound[] = []
  fixture({ step: 'node', hysteria2: false }, {
    'GET /inbounds': () => Response.json(nodes),
    '/inbounds/suggest': () => Response.json({ protocol: inbound.protocol, port: inbound.port, options: inbound.options }),
    '/reality/recommend': () => Response.json({ targets: [{ server: 'www.example.com', latency_ms: 12, ok: true, tls13: true, h2: true }] }),
    'POST /inbounds': async () => { const response = await pending.promise; nodes.push(inbound); return response },
    'PUT /setup/status': () => failure(),
  })
  const { user } = mount()
  expect(await screen.findByRole('button', { name: 'www.example.com · 12 ms' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.setup.createNext }))
  expect(screen.getByRole('button', { name: t.setup.back })).toBeDisabled()
  expect(screen.getByRole('button', { name: t.setup.skip })).toBeDisabled()
  pending.resolve(Response.json(inbound))
  expect(await screen.findByText('Try again')).toBeVisible()
  expect(screen.queryByLabelText(t.inbounds.target)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: t.setup.next })).toBeEnabled()
})

it('shows an auto QR, local copying and official clients for each platform, then clears secret links on exit', async () => {
  fixture({ step: 'finish' })
  const { user, client } = mount()
  expect(await screen.findByRole('img', { name: sub.qr })).toBeVisible()
  expect(screen.getByLabelText(t.users.auto)).toHaveValue(links.subscription.auto)
  for (const platform of platforms) {
    await user.click(screen.getByRole('tab', { name: platform }))
    const panel = screen.getByRole('tabpanel')
    const data = clients.find((item) => item.platforms.includes(platform))!
    expect(within(panel).getByRole('link', { name: t.setup.download })).toHaveAttribute('href', data.download)
    const importURL = within(panel).getByRole('link', { name: t.setup.import }).getAttribute('href')!
    expect(importURL).toContain(encodeURIComponent(links.subscription[data.formats[0]]))
    expect(within(panel).getAllByRole('listitem')).toHaveLength(3)
  }
  await user.click(screen.getByRole('button', { name: t.setup.enter }))
  await screen.findByRole('heading', { name: t.pages.home })
  await waitFor(() => expect(client.getQueryData(['users', '1', 'links'])).toBeUndefined())
})

it('does not present an empty subscription as ready', async () => {
  fixture({ step: 'finish' }, { '/users/1/links': () => Response.json({ ...links, nodes: [] }) })
  mount()
  expect(await screen.findByText(t.setup.noNodes)).toBeVisible()
  expect(screen.queryByRole('img', { name: sub.qr })).not.toBeInTheDocument()
})

it('probes only TCP ports and includes UDP hopping in the security group checklist', async () => {
  const f = fixture({ step: 'firewall' }, {
    '/inbounds': () => Response.json([inbound, { ...hy2, options: { ...hy2.options, port_hopping: { enabled: true, start: 20000, end: 30000 } } }]),
    '/system/port-probe': (init) => Response.json({ port: JSON.parse(String(init?.body)).port, results: [{ ip: '203.0.113.1', ok: true }], hint: 'Check externally too' }),
  })
  const { user } = mount()
  expect(await screen.findByText('20000–30000/UDP')).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.setup.probe }))
  expect(await screen.findAllByText('Check externally too')).toHaveLength(2)
  const bodies = f.fetch.mock.calls.filter(([url]) => url.endsWith('/system/port-probe')).map(([, init]) => JSON.parse(String(init?.body)))
  expect(bodies).toEqual([{ port: 2053 }, { port: 443 }])
})


it('keeps the suggested port on failed Reality detection and shows only one error after a failed create', async () => {
  fixture({ step: 'node', hysteria2: false }, {
    '/inbounds': () => Response.json([]),
    '/inbounds/suggest': () => Response.json({ protocol: 'vless_reality', port: 23456, options: { ...inbound.options, server_name: '', handshake_server: '' }, hint: 'Enter a Reality target manually' }),
    '/reality/recommend': () => Response.json({ targets: [] }),
    'POST /inbounds': () => failure(422),
  })
  const { user } = mount()
  await user.type(await screen.findByLabelText(t.inbounds.target), 'manual.example.com')
  expect(screen.getByLabelText(t.inbounds.port)).toHaveValue(23456)
  expect(screen.getByText('Enter a Reality target manually')).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.setup.createNext }))
  await screen.findByText('Try again')
  expect(screen.getAllByRole('alert')).toHaveLength(1)
  expect(screen.queryByText('Enter a Reality target manually')).not.toBeInTheDocument()
})

it('lists ACME TCP 80 and verified official documentation without probing the temporary listener', async () => {
  const requested: number[] = []
  fixture({ step: 'firewall' }, {
    '/settings': () => Response.json({ ...settings, panel_tls_mode: 'acme' }),
    '/system/port-probe': (init) => { const port = JSON.parse(String(init.body)).port; requested.push(port); return Response.json({ port, results: [], hint: 'Local only' }) },
  })
  const { user } = mount()
  expect(await screen.findByText('80/TCP')).toBeVisible()
  expect(screen.getByText(t.cert.portHint)).toBeVisible()
  for (const provider of t.setup.providers) {
    await user.click(screen.getByText(provider.name))
    const link = screen.getByText(provider.name).parentElement?.querySelector('a')
    expect(link).toHaveAttribute('href', provider.url)
    expect(link).toHaveAttribute('rel', 'noreferrer')
  }
  await user.click(screen.getByRole('button', { name: t.setup.probe }))
  await waitFor(() => expect(requested.length).toBeGreaterThan(0))
  expect(requested).not.toContain(80)
  expect(requested).toEqual(expect.arrayContaining([2053, 443]))
})
