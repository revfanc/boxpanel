import { screen, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { DashboardPage } from '@/pages/dashboard'
import { AlertSettingsPanel, ServerAlertPanel } from '@/components/AlertSettings'
import { billing, overview, alertSettings, mockAPI, mountPage } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

const a = t.alerts

it('shows billing, unread events and failed notifications even when system metrics fail', async () => {
  let read = false
  const fetch = mockAPI({
    '/overview': () => Response.json({
      ...overview,
      servers: [{ ...overview.servers[0], system: { ...overview.servers[0]!.system, metrics: null },
        billing: { ...billing, supported: true, interface: 'eth0', sampled_at: 1735689600, used: 950e9, quota: 1e12, percent: 95 } }],
      unread_count: read ? 0 : 1,
      unread: read ? [] : [{ key: 'hash', kind: 'user_quota_80', address: 'node.example.com', username: 'alice', used: 80, limit: 100, created_at: 1735689600 }],
      recent: [{ id: 1, channel: 'bark', kind: 'test', status: 'failed', error: 'timeout', updated_at: 1735689600, attempts: 8, grouped: false }],
    }),
    'POST /alerts/events/hash/read': () => { read = true; return new Response(null, { status: 204 }) },
  })
  const { user } = mountPage(<DashboardPage />)
  expect(await screen.findByText(/95\.0%/)).toBeVisible()
  expect(screen.getByText(a.failure.timeout)).toBeVisible()
  expect(screen.getByText(`${a.event.user_quota_80} · alice`)).toBeVisible()
  await user.click(screen.getByRole('button', { name: a.markRead }))
  expect(await screen.findByText(a.emptyInbox)).toBeVisible()
  expect(screen.getByText(a.failure.timeout)).toBeVisible()
  expect(fetch.mock.calls.some(([url, init]) => url.endsWith('/alerts/events/hash/read') && init?.method === 'POST')).toBe(true)
})

it.each([1, 2])('uses the same billing form and saves thresholds for server %i', async (id) => {
  const path = '/servers/' + id + '/alerts'
  const fetch = mockAPI({ ['PUT ' + path]: (init) => Response.json(JSON.parse(String(init.body))) })
  const { user } = mountPage(<ServerAlertPanel serverID={id} />)
  const quota = await screen.findByLabelText(a.quota)
  await user.clear(quota)
  await user.type(quota, '500')
  await user.click(screen.getByRole('combobox', { name: a.mode }))
  await user.click(await screen.findByRole('option', { name: a.modes.both }))
  await user.click(screen.getByRole('switch', { name: a.event.server_quota_90 }))
  expect(screen.queryByRole('switch', { name: a.offlineNotifications })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: a.saveSettings }))
  expect(await screen.findByText(a.settingsSaved)).toBeVisible()
  const call = fetch.mock.calls.find(([url, init]) => url.endsWith(path) && init?.method === 'PUT')
  expect(JSON.parse(String(call?.[1]?.body))).toMatchObject({ quota_gb: 500, billing_mode: 'both', events: { server_quota_90: false } })
})

it('saves global availability notifications without editing a server quota', async () => {
  const fetch = mockAPI({ 'PUT /alerts/settings': (init) => Response.json({ ...alertSettings.config, ...JSON.parse(String(init.body)) }) })
  const { user } = mountPage(<AlertSettingsPanel />)
  const availability = await screen.findByRole('switch', { name: a.offlineNotifications })
  expect(availability).toBeChecked()
  expect(screen.queryByLabelText(a.quota)).not.toBeInTheDocument()
  await user.click(availability)
  await user.click(screen.getByRole('button', { name: a.saveEvents }))
  expect(await screen.findByText(t.common.saved)).toBeVisible()
  const call = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')
  const payload = JSON.parse(String(call?.[1]?.body))
  expect(payload).toEqual({ events: expect.objectContaining({ server_offline: false }) })
  expect(payload.events).not.toHaveProperty('server_quota_90')
})

it('shows remote recovery with its duration in the dashboard inbox', async () => {
  mockAPI({ '/overview': () => Response.json({ ...overview, unread_count: 1, unread: [
    { key: 'recovered', kind: 'server_recovered', server_id: 2, address: 'edge.example.com', used: 0, limit: 0,
      created_at: 1735689600, offline_seconds: 120 },
  ] }) })
  mountPage(<DashboardPage />)
  expect(await screen.findByText(a.event.server_recovered)).toBeVisible()
  expect(screen.getByText(/edge\.example\.com/)).toBeVisible()
  expect(screen.getByText(a.offlineDuration('2 分钟'))).toBeVisible()
})

it('preserves masked values on save and sends a test with saved credentials', async () => {
  const settings = { ...alertSettings, channels: { ...alertSettings.channels, telegram: { enabled: false, credential_mask: '****1234', chat_id_mask: '****4321' } } }
  const fetch = mockAPI({
    '/alerts/settings': () => Response.json(settings),
    'PUT /alerts/channels/telegram': () => Response.json({ ...settings.channels.telegram, enabled: true }),
    'POST /alerts/channels/telegram/test': () => Response.json({ id: 42 }, { status: 202 }),
  })
  const { user, client } = mountPage(<AlertSettingsPanel />)
  const heading = await screen.findByRole('heading', { name: a.channelNames.telegram })
  const form = within(heading.closest('form')!)
  const token = form.getByLabelText(a.credentials.telegram)
  expect(token).toHaveValue('')
  expect(token).toHaveAttribute('placeholder', '****1234')
  await user.click(form.getByRole('switch', { name: a.channelEnabled(a.channelNames.telegram) }))
  expect(form.getByRole('button', { name: a.sendTest })).toBeDisabled()
  await user.click(form.getByRole('button', { name: a.channelSave(a.channelNames.telegram) }))
  expect(await form.findByText(a.channelSaved)).toBeVisible()
  const call = fetch.mock.calls.find(([url, init]) => url.endsWith('/alerts/channels/telegram') && init?.method === 'PUT')
  expect(JSON.parse(String(call?.[1]?.body))).toEqual({ enabled: true })
  await user.click(form.getByRole('button', { name: a.sendTest }))
  expect(await form.findByText(a.testQueued)).toBeVisible()
  expect(client.getMutationCache().getAll()).toHaveLength(0)
})
