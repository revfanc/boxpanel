import { act, screen, waitFor, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ServerDetailPage } from './detail'
import { alertSettings, billing, deferred, edge, failure, inbound, mockAPI, mountPage, server, status } from '@/test/resources'
import { serverStatusQuery } from '@/lib/resources'
import { zhCN as t } from '@/i18n/zh-CN'
import type { RestartState } from '@/types/api'

it.each([server, edge])('uses the same tabs and server scope for $name', async (target) => {
  const path = '/servers/' + target.id
  const local = target.is_local
  const scoped = { ...inbound, id: 9, server_id: target.id, remark: 'Scoped inbound' }
  const fetch = mockAPI({
    '/inbounds': () => Response.json([scoped, { ...inbound, server_id: local ? edge.id : server.id }]),
    [path + '/traffic']: () => Response.json({
      server_id: target.id, user_traffic: { up: 1024, down: 2048 },
      billing: { ...billing, supported: true, sampled_at: 1735689600, used: 800e6, quota: 1e9, percent: 80, interface: 'eth0' },
      users: [{ id: 1, name: 'alice', up: 1024, down: 2048 }],
    }),
    [path + '/alerts']: () => Response.json({ ...alertSettings, interfaces: [local ? 'eth0' : 'eth1'] }),
    [path + '/logs']: () => Response.json({ ready: true, lines: [target.name + ' log line'] }),
  })
  const { user, router } = mountPage(<ServerDetailPage />, path, '/servers/:id')
  const list = await screen.findByRole('tablist', { name: t.servers.detail })
  expect(list).toHaveClass('flex-wrap')
  expect(within(list).getAllByRole('tab').map((tab) => tab.textContent)).toEqual(Object.values(t.servers.tabs))
  expect(await screen.findByText('12.5%')).toBeVisible()

  const select = async (tab: keyof typeof t.servers.tabs) => {
    await user.click(within(list).getByRole('tab', { name: t.servers.tabs[tab] }))
    expect(router.state.location.search).toBe(tab === 'status' ? '' : '?tab=' + tab)
    expect(within(list).getByRole('tab', { name: t.servers.tabs[tab] })).toHaveAttribute('aria-selected', 'true')
  }
  await select('inbounds')
  expect(await screen.findByText(scoped.remark)).toBeVisible()
  expect(screen.queryByText(inbound.remark)).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: t.pages.newInbound })).toHaveAttribute('href', '/inbounds/new?server=' + target.id)
  expect(screen.getByRole('link', { name: t.common.edit })).toHaveAttribute('href', '/inbounds/9?server=' + target.id)

  await select('traffic')
  expect(await screen.findByText('80.0%')).toBeVisible()
  expect(screen.getByText(/^2025-01-01 00:00:00 UTC/)).toBeVisible()
  expect(screen.getByText('3 KiB')).toBeVisible()
  const table = within(screen.getByRole('table', { name: t.users.usageByUser }))
  expect(table.getByRole('link', { name: 'alice' })).toHaveAttribute('href', '/users/1?tab=traffic')
  expect(table.getByRole('cell', { name: '2 KiB' })).toBeVisible()

  await select('alerts')
  const network = await screen.findByLabelText(t.alerts.network)
  expect(document.getElementById(network.getAttribute('list')!)?.querySelector('option')).toHaveValue(local ? 'eth0' : 'eth1')
  expect(screen.getByRole('switch', { name: t.alerts.event.server_quota_90 })).toBeVisible()

  await select('logs')
  expect(await screen.findByText(target.name + ' log line')).toBeVisible()
  await select('status')
  if (!local) {
    await waitFor(() => expect(fetch.mock.calls.some(([url, init]) => url.includes(path + '/logs/') && init?.method === 'DELETE')).toBe(true))
  }
})

it('shows local restart errors, then the completed checked restart', async () => {
  const response = deferred<Response>()
  let fail = true
  const fetch = mockAPI({ 'POST /servers/1/restart': () => fail ? response.promise : new Response(null, { status: 204 }) })
  const { user } = mountPage(<ServerDetailPage />, '/servers/1', '/servers/:id')
  const restart = await screen.findByRole('button', { name: t.servers.restart })
  await user.click(restart)
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: t.common.confirm }))
  expect(screen.getByRole('button', { name: t.common.working })).toBeDisabled()
  await act(async () => response.resolve(failure(422, 'singbox_check_failed')))
  expect(await within(screen.getByRole('alertdialog')).findByText('Try again')).toBeVisible()
  fail = false
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: t.common.confirm }))
  expect(await screen.findByText(t.servers.restarted)).toBeVisible()
})

it('shows remote restart progress and the result reported by the heartbeat', async () => {
  let state: RestartState | null = null
  mockAPI({
    '/servers/2/status': () => Response.json({ ...status, server_id: 2, restart: state }),
    'POST /servers/2/restart': () => { state = { id: 'command', status: 'pending' }; return Response.json(state, { status: 202 }) },
  })
  const { user, client } = mountPage(<ServerDetailPage />, '/servers/2', '/servers/:id')
  await user.click(await screen.findByRole('button', { name: t.servers.restart }))
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  expect(await screen.findByText(t.servers.restartPending)).toBeVisible()
  expect(screen.getByRole('button', { name: t.servers.restart })).toBeDisabled()
  state = { id: 'command', status: 'succeeded' }
  await act(() => client.invalidateQueries({ queryKey: serverStatusQuery(2).queryKey }))
  expect(await screen.findByText(t.servers.restarted)).toBeVisible()
})

it('starts the master upgrade only after confirmation and reports the installed version', async () => {
  const result = { state: 'running', current: 'v0.6.0', target: 'v0.6.1' }
  const fetch = mockAPI({
    'POST /system/upgrade': () => Response.json(result, { status: 202 }),
    '/system/upgrade': () => Response.json(result.state === 'running' ? { state: 'idle' } : result),
  })
  const { user, client } = mountPage(<ServerDetailPage />, '/servers/1', '/servers/:id')
  await user.click(await screen.findByRole('button', { name: t.servers.localUpgrade }))
  const writes = () => fetch.mock.calls.filter(([url, init]) => url.endsWith('/system/upgrade') && init?.method === 'POST')
  expect(writes()).toHaveLength(0)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: t.common.confirm }))
  expect(await screen.findByText(t.servers.upgradeRunning(result.target))).toBeVisible()
  expect(screen.getByRole('button', { name: t.servers.localUpgrade })).toBeDisabled()
  expect(writes()).toHaveLength(1)
  result.state = 'succeeded'
  result.current = result.target
  await act(async () => { await client.invalidateQueries({ queryKey: ['system', 'upgrade'] }) })
  expect(await screen.findByText(t.servers.upgradeSucceeded(result.target))).toBeVisible()
})
