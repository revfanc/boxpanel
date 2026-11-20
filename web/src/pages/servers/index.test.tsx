import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ServersPage } from '.'
import { zhCN as t } from '@/i18n/zh-CN'
import { alertSettings, failure, mockAPI, mountPage, server } from '@/test/resources'

it.each([
  ['v0.5.0-rc.2', 'v0.5.0-rc.2', ' --version v0.5.0-rc.2', false],
  ['dev', 'main', '', true],
] as const)('offers the %s upgrade command and explains protocol errors', async (masterVersion, ref, suffix, outdated) => {
  mockAPI({ '/servers': () => Response.json([
    { ...server, version: masterVersion },
    { ...server, id: 2, is_local: false, name: 'Edge', version: outdated ? masterVersion : 'v0.4.0',
      upgrade_required: outdated, sync_error: outdated ? '' : 'master_outdated' },
  ]) })
  const { user } = mountPage(<ServersPage />)
  const clipboard = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  const command = `bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/${ref}/scripts/install.sh) update${suffix}`
  expect(await screen.findByLabelText(t.servers.upgradeCommand)).toHaveValue(command)
  expect(screen.getByText(outdated ? t.servers.nodeOutdated : t.servers.syncErrors.master_outdated)).toBeVisible()
  await user.click(screen.getByRole('button', { name: `${t.common.copy} ${t.servers.upgradeCommand}` }))
  expect(clipboard).toHaveBeenCalledWith(command)
})

it('opens and saves billing settings for the selected remote server', async () => {
  const fetch = mockAPI({
    '/servers': () => Response.json([server, { ...server, id: 2, is_local: false, name: 'Edge' }]),
    '/alerts/settings?server_id=2': () => Response.json({ ...alertSettings, interfaces: ['eth1'],
      billing: { ...alertSettings.billing, supported: true, sampled_at: 1735689600, interface: 'eth1', used: 800e6, quota: 1e9, percent: 80 },
    }),
    'PUT /alerts/settings': (init) => Response.json(JSON.parse(String(init.body))),
  })
  const { user } = mountPage(<ServersPage />)
  const button = await screen.findByRole('button', { name: t.servers.trafficAlerts })
  expect(button).toHaveAttribute('aria-expanded', 'false')
  await user.click(button)
  expect(await screen.findByText('80.0%')).toBeVisible()
  expect(screen.getByRole('switch', { name: t.alerts.event.server_quota_80 })).toBeChecked()
  expect(screen.queryByRole('switch', { name: t.alerts.offlineNotifications })).not.toBeInTheDocument()
  const field = screen.getByLabelText(t.alerts.network)
  expect(document.getElementById(field.getAttribute('list')!)?.querySelector('option')).toHaveValue('eth1')
  await user.type(field, 'eth1')
  await user.click(screen.getByRole('button', { name: t.alerts.saveSettings }))
  expect(await screen.findByText(t.alerts.settingsSaved)).toBeVisible()
  const put = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')
  expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({ server_id: 2, interface: 'eth1' })
  await user.click(button)
  expect(screen.queryByLabelText(t.alerts.network)).not.toBeInTheDocument()
})

it('shows server metadata and preserves the name while retrying a failed rename', async () => {
  let fail = true
  const fetch = mockAPI({
    'PUT /servers/1': (init) => fail ? failure(422) : Response.json({ ...server, ...JSON.parse(init.body as string) }),
  })
  const { user } = mountPage(<ServersPage />)
  expect(await screen.findByText(server.public_host)).toBeVisible()
  expect(screen.getByText(server.version)).toBeVisible()
  expect(screen.getByText(t.servers.status)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.servers.rename }))
  const input = screen.getByRole('textbox', { name: t.servers.name })
  expect(input).toHaveAccessibleDescription(t.servers.renameHint)
  await user.clear(input)
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(t.servers.invalidName)).toBeVisible()
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0)
  await user.type(input, '  新名称  ')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText('Try again')).toBeVisible()
  expect(input).toHaveValue('  新名称  ')
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText('新名称')).toBeVisible()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
})

it('generates, copies and revokes an enrollment command without caching the token', async () => {
  const token = 'one-time-test-token'
  const pin = 'test-spki-pin'
  const record = { id: 3, name: '边缘', expires_at: 1800000000 }
  let records: typeof record[] = []
  const fetch = mockAPI({
    '/servers/join-tokens': () => Response.json(records),
    'POST /servers/join-tokens': (init) => {
      expect(JSON.parse(init.body as string)).toEqual({ name: record.name })
      records = [record]
      return Response.json({ ...record, token, master_url: 'https://master.test/panel/', master_pin: pin }, { status: 201 })
    },
    'DELETE /servers/join-tokens/3': () => {
      records = []
      return new Response(null, { status: 204 })
    },
    '/servers/join-tokens/3': () => failure(404, 'not_found'),
  })
  const { client, user } = mountPage(<ServersPage />)
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  await user.click(screen.getByRole('button', { name: t.servers.add }))
  await user.type(await screen.findByRole('textbox', { name: t.servers.name }), record.name)
  await user.click(screen.getByRole('button', { name: t.servers.generate }))
  const command = await (screen.findByRole('textbox', { name: t.servers.command }) as Promise<HTMLTextAreaElement>)
  expect(command.value).toContain("--join 'https://master.test/panel/'")
  expect(command.value).toContain(`--token '${token}' --master-pin '${pin}'`)
  expect(client.getQueryData(['servers', 'join-tokens'])).toEqual([record])
  await user.click(screen.getByRole('button', { name: `${t.common.copy} ${t.servers.command}` }))
  expect(copy).toHaveBeenCalledWith(command.value)
  await user.click(screen.getByRole('button', { name: `${t.servers.revoke} ${record.name}` }))
  expect(await screen.findByText(t.servers.noTokens)).toBeVisible()
  expect(await screen.findByText(t.servers.joinUnavailable)).toBeVisible()
  expect(screen.queryByRole('textbox', { name: t.servers.command })).not.toBeInTheDocument()
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(true)
})

it('waits for the matching server heartbeat even when another server with the same name appears', async () => {
  const record = { id: 9, name: 'Edge', expires_at: 1800000000 }
  const matching = { ...server, id: 3, is_local: false, name: record.name, public_host: 'right.example', version: 'v0.5.0', status: 'online', last_seen_at: 1700000000 }
  const unrelated = { ...matching, id: 2, public_host: 'wrong.example' }
  let registered = false
  let heartbeat = false
  const fetch = mockAPI({
    '/servers': () => Response.json([server, unrelated, ...(registered ? [heartbeat ? matching : { ...matching, status: 'pending', last_seen_at: null }] : [])]),
    '/servers/join-tokens': () => Response.json(registered ? [] : [record]),
    'POST /servers/join-tokens': () => Response.json({ ...record, token: 'test', master_url: 'https://master.test/panel/', master_pin: 'test-pin' }),
    '/servers/join-tokens/9': () => Response.json(registered ? { ...record, server_id: matching.id } : record),
  })
  const { user, router } = mountPage(<ServersPage />)
  await user.click(screen.getByRole('button', { name: t.servers.add }))
  expect(screen.getByText(`1. ${t.servers.joinSteps[0]}`)).toHaveAttribute('aria-current', 'step')
  await user.type(screen.getByRole('textbox', { name: t.servers.name }), record.name)
  await user.click(screen.getByRole('button', { name: t.servers.generate }))
  await screen.findByLabelText(t.servers.command)
  expect(screen.getByText(`2. ${t.servers.joinSteps[1]}`)).toHaveAttribute('aria-current', 'step')
  expect(screen.getByText(t.servers.joinNAT)).toBeVisible()
  vi.useFakeTimers()
  try {
    fireEvent.click(screen.getByRole('button', { name: t.servers.waitForNode }))
    expect(screen.getByText(`3. ${t.servers.joinSteps[2]}`)).toHaveAttribute('aria-current', 'step')
    expect(screen.getByText(t.servers.waiting)).toBeVisible()
    await act(() => vi.advanceTimersByTimeAsync(20))
    expect(screen.queryByRole('link', { name: t.servers.createInbound })).not.toBeInTheDocument()
    registered = true
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText(t.servers.waiting)).toBeVisible()
    heartbeat = true
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText(t.servers.connected(matching.name, matching.public_host, matching.version))).toBeVisible()
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/servers/join-tokens/9'))).toHaveLength(3)
    await act(() => vi.advanceTimersByTimeAsync(6000))
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/servers/join-tokens/9'))).toHaveLength(3)
    fireEvent.click(screen.getByRole('link', { name: t.servers.createInbound }))
    expect(router.state.location).toMatchObject({ pathname: '/inbounds/new', search: '?server=3' })
  } finally { vi.useRealTimers() }
})

it.each(['expired', 'closed'] as const)('stops the enrollment poll when the wizard is %s', async (scenario) => {
  const record = { id: 9, name: 'Edge', expires_at: 1800000000 }
  let expired = false
  const fetch = mockAPI({
    '/servers/join-tokens': () => Response.json(expired ? [] : [record]),
    'POST /servers/join-tokens': () => Response.json({ ...record, token: 'test', master_url: 'https://master.test/', master_pin: 'test-pin' }),
    '/servers/join-tokens/9': () => expired ? failure(404, 'not_found') : Response.json(record),
  })
  const { user } = mountPage(<ServersPage />)
  await user.click(screen.getByRole('button', { name: t.servers.add }))
  await user.type(screen.getByRole('textbox', { name: t.servers.name }), record.name)
  await user.click(screen.getByRole('button', { name: t.servers.generate }))
  await screen.findByLabelText(t.servers.command)
  vi.useFakeTimers()
  try {
    fireEvent.click(screen.getByRole('button', { name: t.servers.waitForNode }))
    await act(() => vi.advanceTimersByTimeAsync(20))
    const polls = () => fetch.mock.calls.filter(([url]) => url.endsWith('/servers/join-tokens/9')).length
    const serverReads = () => fetch.mock.calls.filter(([url]) => url.endsWith('/servers')).length
    expect(polls()).toBe(1)
    if (scenario === 'expired') {
      expired = true
      await act(() => vi.advanceTimersByTimeAsync(3000))
      expect(screen.getByText(t.servers.joinUnavailable)).toBeVisible()
      expect(polls()).toBe(2)
    } else {
      fireEvent.click(screen.getByRole('button', { name: t.servers.closeAdd }))
      expect(screen.queryByText(t.servers.waiting)).not.toBeInTheDocument()
    }
    const count = polls()
    const reads = serverReads()
    await act(() => vi.advanceTimersByTimeAsync(9000))
    expect(polls()).toBe(count)
    expect(serverReads()).toBe(reads)
    if (expired) {
      fireEvent.click(screen.getByRole('button', { name: t.servers.regenerate }))
      expect(screen.getByRole('textbox', { name: t.servers.name })).toHaveValue(record.name)
    }
  } finally { vi.useRealTimers() }
})

it('confirms removal for remote servers and refreshes the list', async () => {
  const edge = { ...server, id: 2, name: '远程服务器', is_local: false, status: 'pending', public_host: '198.51.100.1' }
  let servers = [server, edge]
  const fetch = mockAPI({
    '/servers': () => Response.json(servers),
    'DELETE /servers/2': () => {
      servers = [server]
      return new Response(null, { status: 204 })
    },
  })
  const { user } = mountPage(<ServersPage />)
  expect(await screen.findByText(t.servers.noHeartbeat)).toBeVisible()
  const open = screen.getByRole('button', { name: t.servers.remove })
  await user.click(open)
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByText(t.servers.removeHint(edge.name))).toBeVisible()
  await user.click(within(dialog).getByRole('button', { name: t.common.cancel }))
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
  await user.click(open)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: t.common.confirm }))
  await waitFor(() => expect(screen.queryByText(edge.name)).not.toBeInTheDocument())
  expect(screen.queryByRole('button', { name: t.servers.remove })).not.toBeInTheDocument()
})

it('shows remote sync and traffic totals while updating the subscription host', async () => {
  const edge = {
    ...server, id: 2, name: 'Edge', is_local: false, status: 'online', public_host: 'edge.example.com',
    desired_generation: 7, applied_generation: 6, sync_status: 'failed', sync_error: 'port_in_use', singbox_active: true,
  }
  const fetch = mockAPI({
    '/servers': () => Response.json([server, { ...edge, traffic_up: 1024, traffic_down: 2 * 1024 ** 2 }]),
    'PUT /servers/2': (init) => Response.json({ ...edge, ...JSON.parse(String(init.body)) }),
  })
  const { user } = mountPage(<ServersPage />)
  expect(await screen.findByText(t.servers.syncStatuses.failed, { selector: '[data-slot="badge"]' })).toBeVisible()
  expect(screen.getByText('6 / 7')).toBeVisible()
  expect(screen.getByText(t.servers.syncErrors.port_in_use)).toBeVisible()
  expect(screen.getByText(t.servers.traffic)).toBeVisible()
  expect(screen.getByText('1 KiB / 2 MiB')).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.common.edit }))
  const input = screen.getByRole('textbox', { name: t.servers.address })
  await user.clear(input)
  await user.type(input, 'new.edge.example.com')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText('new.edge.example.com')).toBeVisible()
  expect(screen.getByText('1 KiB / 2 MiB')).toBeVisible()
  const request = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')
  expect(JSON.parse(String(request?.[1]?.body))).toMatchObject({ public_host: 'new.edge.example.com' })
})

it('counts server states independently and shows the card badges, heartbeat and node links', async () => {
  const now = Math.floor(Date.now() / 1000)
  const online = { ...server, id: 2, name: 'Online', is_local: false, status: 'online', version: 'v0.5.0', last_seen_at: now - 12, uptime: 5, inbound_counts: { ready: 3, pending: 1 } }
  const offline = { ...online, id: 3, name: 'Offline', status: 'offline', sync_status: 'failed', uptime: undefined }
  const pending = { ...online, id: 4, name: 'Pending', status: 'pending', last_seen_at: null, uptime: undefined }
  mockAPI({ '/servers': () => Response.json([server, online, offline, pending]) })
  mountPage(<ServersPage />)
  await screen.findByText(t.servers.total)
  for (const [label, count] of [[t.servers.total, 4], [t.servers.statuses.online, 2], [t.servers.statuses.offline, 1], [t.servers.syncStatuses.failed, 1]] as const) {
    expect(screen.getByText(label, { selector: 'dt' }).nextElementSibling).toHaveTextContent(String(count))
  }
  for (const [label, color] of [[t.servers.statuses.local, 'text-emerald-800'], [t.servers.statuses.online, 'text-emerald-800'], [t.servers.statuses.offline, 'text-amber-800'], [t.servers.statuses.pending, 'text-muted-foreground'], [t.servers.syncStatuses.failed, 'text-red-800']] as const) {
    expect(screen.getByText(label, { selector: '[data-slot="badge"]' })).toHaveClass(color)
  }
  const card = within(screen.getByText(online.name).closest<HTMLElement>('[data-slot="card"]')!)
  const time = card.getByText(/1[23] 秒前/)
  expect(time).toHaveAttribute('title', new Date((now - 12) * 1000).toLocaleString('zh-CN'))
  expect(card.getByText('3 / 1')).toBeVisible()
  expect(card.getByText(t.servers.uptimeSeconds(5))).toBeVisible()
  expect(card.getByText(t.servers.versionDifference(server.version))).toBeVisible()
  expect(card.getByRole('link', { name: t.servers.viewInbounds })).toHaveAttribute('href', '/inbounds?server=2')
  expect(screen.getAllByText(t.servers.uptime)).toHaveLength(1)
})

it('says an offline server waits to come online instead of syncing', async () => {
  const offline = { ...server, id: 3, name: 'Offline', is_local: false, status: 'offline', version: 'v0.5.0', last_seen_at: 1, sync_status: 'syncing', desired_generation: 4, applied_generation: 3, singbox_active: true, inbound_counts: { ready: 1, pending: 0 } }
  mockAPI({ '/servers': () => Response.json([server, offline]) })
  mountPage(<ServersPage />)
  const card = within((await screen.findByText(offline.name)).closest<HTMLElement>('[data-slot="card"]')!)
  expect(card.getByText(t.servers.syncWaitingOnline)).toBeVisible()
  expect(card.queryByText(t.servers.syncStatuses.syncing)).toBeNull()
})
