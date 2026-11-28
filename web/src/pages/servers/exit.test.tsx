import { act, screen, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ServerDetailPage } from './detail'
import { ServerExitTab } from './exit'
import { serverExitQuery } from '@/lib/resources'
import { billing, mockAPI, mountPage } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'
import type { ExitSettings, ServerExit } from '@/types/api'

const ex = t.exitServers
const destination = { id: 2, name: 'Exit machine', address: '8.8.4.4' }
const legacy = { id: 3, name: 'Legacy machine', address: '8.8.8.8', unavailable_reason: 'upgrade_required' } as const
const blank: ServerExit = { external_supported: true, server_id: 1, supported: true, address: '9.9.9.9', candidates: [destination, legacy], link: null, forwarded: { up: 0, down: 0 } }
const initial: ExitSettings = { exit_server_id: 2, mode: 'split', failure: 'block', rules: { presets: ['ai'], domains: ['api.example.com'], domain_suffixes: ['example.org'] } }
const noContent = () => new Response(null, { status: 204 })

it('tests a draft proxy, shows its egress IP and saves it as the exit', async () => {
  let saved: unknown
  const fetch = mockAPI({
    '/servers/1/exit': () => Response.json({ ...blank, address: '', candidates: [] }),
    'POST /servers/1/exit/test': () => Response.json({ ip: '8.8.4.4' }),
    'PUT /servers/1/exit': (init) => { saved = JSON.parse(String(init.body)); return noContent() },
  })
  const { user } = mountPage(<ServerExitTab serverID={1} />)
  await user.click(await screen.findByRole('combobox', { name: ex.target }))
  await user.click(await screen.findByRole('option', { name: ex.external }))
  await user.click(screen.getByRole('combobox', { name: ex.proxyProtocol }))
  await user.click(await screen.findByRole('option', { name: 'HTTPS' }))
  await user.type(screen.getByLabelText(ex.proxyHost), 'proxy.example.org')
  await user.type(screen.getByLabelText(ex.proxyPort), '443')
  await user.click(screen.getByRole('button', { name: ex.proxyTest }))
  expect(await screen.findByText(ex.proxyIP('8.8.4.4'))).toBeVisible()
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  await user.type(screen.getByLabelText(ex.proxyUser), 'alice')
  expect(screen.queryByText(ex.proxyIP('8.8.4.4'))).not.toBeInTheDocument()
  await user.type(screen.getByLabelText(ex.proxyPassword), 'test-pass')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(ex.saved)).toBeVisible()
  expect(saved).toMatchObject({ external_proxy: { type: 'https', host: 'proxy.example.org', port: 443, username: 'alice', password: 'test-pass' } })
})

it('saves the default policy after selecting only a server', async () => {
  let saved: ExitSettings | undefined
  mockAPI({
    '/servers/1/exit': () => Response.json(blank),
    'PUT /servers/1/exit': (init) => { saved = JSON.parse(String(init.body)) as ExitSettings; return noContent() },
  })
  const { user } = mountPage(<ServerExitTab serverID={1} />, '/servers/1')
  await user.click(await screen.findByRole('combobox', { name: ex.target }))
  await user.click(await screen.findByRole('option', { name: destination.name }))
  expect(screen.getByText(ex.policyOptions).closest('details')).not.toHaveAttribute('open')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(ex.saved)).toBeVisible()
  expect(saved).toEqual({ exit_server_id: 2, mode: 'split', failure: 'block', rules: { presets: ['ai', 'streaming'], domains: [], domain_suffixes: [] } })
})

it('edits server rules, persists failure policy and keeps rules when switching to all traffic', async () => {
  let saved: ExitSettings | undefined
  const fetch = mockAPI({
    '/servers/1/exit': () => Response.json({ ...blank, link: saved ? { ...saved, status: 'checking', checked_at: null, port: 20000 } : null }),
    'PUT /servers/1/exit': (init) => { saved = JSON.parse(String(init.body)) as ExitSettings; return noContent() },
  })
  const { user } = mountPage(<ServerDetailPage />, '/servers/1?tab=exit', '/servers/:id')
  expect(await screen.findByText(ex.direct)).toBeVisible()
  expect(screen.getByRole('tab', { name: t.servers.tabs.exit })).toHaveAttribute('aria-selected', 'true')

  await user.click(screen.getByRole('combobox', { name: ex.target }))
  expect(await screen.findByRole('option', { name: ex.optionUnavailable(legacy.name, ex.reasons.upgrade_required) })).toHaveAttribute('aria-disabled', 'true')
  await user.click(screen.getByRole('option', { name: destination.name }))

  await user.click(screen.getByText(ex.policyOptions))
  expect(screen.getByRole('radio', { name: new RegExp(ex.split) })).toBeChecked()
  await user.click(screen.getByText(ex.customDomains))
  expect(screen.getByRole('combobox', { name: ex.failure })).toHaveTextContent(ex.failures.block)
  await user.click(screen.getByRole('checkbox', { name: ex.presets.streaming }))
  await user.type(screen.getByLabelText(ex.domains), 'https://api.example.com')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(ex.domainsInvalid)).toBeVisible()
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false)
  await user.clear(screen.getByLabelText(ex.domains))
  await user.type(screen.getByLabelText(ex.domains), 'API.Example.com.')
  await user.type(screen.getByLabelText(ex.suffixes), 'example.org')
  await user.click(screen.getByRole('combobox', { name: ex.failure }))
  await user.click(await screen.findByRole('option', { name: ex.failures.direct }))
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(ex.saved)).toBeVisible()
  expect(saved).toEqual({ ...initial, failure: 'direct' })
  expect(screen.getByText(ex.states.checking)).toBeVisible()

  await user.click(screen.getByRole('radio', { name: new RegExp(ex.all) }))
  expect(screen.queryByLabelText(ex.domains)).not.toBeInTheDocument()
  expect(screen.getByText(ex.allHint)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(ex.saved)).toBeVisible()
  expect(saved).toEqual({ ...initial, mode: 'all', failure: 'direct' })
  expect(fetch.mock.calls.some(([url]) => url.endsWith('/exit-address'))).toBe(false)
})

it('displays checked, fallback, stale and recovered status and confirms disable', async () => {
  let link: ServerExit['link'] = { ...initial, status: 'unavailable', checked_at: 1735689600, port: 20000 }
  const fetch = mockAPI({
    '/servers/1/exit': () => Response.json({ ...blank, link }),
    'DELETE /servers/1/exit': () => { link = null; return noContent() },
  })
  const { user, client } = mountPage(<ServerExitTab serverID={1} />)
  expect(await screen.findByText(ex.states.unavailable)).toBeVisible()
  expect(screen.getByText(ex.blockedHint)).toBeVisible()
  expect(screen.getByText(/^最近探测/)).toBeVisible()
  await user.click(screen.getByText(ex.network))
  expect(screen.getByText(ex.cloudFirewall(20000, blank.address))).toBeVisible()
  for (const status of ['fallback', 'policy_pending', 'stale', 'available'] as const) {
    link = { ...initial, status, checked_at: 1735689610, port: 20000 }
    await act(() => client.invalidateQueries({ queryKey: serverExitQuery(1).queryKey }))
    expect(await screen.findByText(ex.states[status])).toBeVisible()
  }
  await user.click(screen.getByRole('button', { name: ex.disable }))
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: t.common.confirm }))
  expect(await screen.findByText(ex.disabled)).toBeVisible()
  expect(screen.getByText(ex.direct)).toBeVisible()
})

it('waits for automatic addresses when an endpoint has no IP', async () => {
  mockAPI({ '/servers/1/exit': () => Response.json({ ...blank, address: '', candidates: [
    { ...destination, address: '', unavailable_reason: 'address_required' },
  ] }) })
  const { user } = mountPage(<ServerExitTab serverID={1} />)
  expect(await screen.findByText(ex.addressRequired)).toBeVisible()
  await user.click(screen.getByRole('combobox', { name: ex.target }))
  expect(await screen.findByRole('option', { name: ex.optionUnavailable(destination.name, ex.reasons.address_required) })).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('button', { name: t.common.save })).toBeDisabled()
})

it('shows exit forwarding in the server traffic tab', async () => {
  mockAPI({ '/servers/2/traffic': () => Response.json({
    server_id: 2, billing, user_traffic: { up: 0, down: 0 }, users: [], exit_traffic: { up: 1024, down: 4096 },
  }) })
  mountPage(<ServerDetailPage />, '/servers/2?tab=traffic', '/servers/:id')
  expect(await screen.findByText(ex.forwarded)).toBeVisible()
  expect(screen.getByText('5 KiB')).toBeVisible()
})
