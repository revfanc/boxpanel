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
const blank: ServerExit = { custom_address: false, server_id: 1, supported: true, address: '9.9.9.9', candidates: [destination, legacy], link: null, forwarded: { up: 0, down: 0 } }
const initial: ExitSettings = { exit_server_id: 2, mode: 'split', failure: 'block', rules: { presets: ['ai'], domains: ['api.example.com'], domain_suffixes: ['example.org'] } }
const noContent = () => new Response(null, { status: 204 })

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

  expect(screen.getByRole('radio', { name: new RegExp(ex.split) })).toBeChecked()
  expect(screen.queryByLabelText(ex.address)).not.toBeInTheDocument()
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

it('allows an explicit link address and hides entry editing for an outdated node', async () => {
  let address = ''
  mockAPI({
    '/servers/1/exit': () => Response.json({ ...blank, address }),
    'PUT /servers/1/exit-address': (init) => { ({ address } = JSON.parse(String(init.body))); return noContent() },
    '/servers/2/exit': () => Response.json({ ...blank, server_id: 2, supported: false }),
  })
  const { user, router } = mountPage(<ServerDetailPage />, '/servers/1?tab=exit', '/servers/:id')
  const field = await screen.findByRole('textbox', { name: ex.address })
  expect(screen.getByText(ex.addressRequired)).toBeVisible()
  await user.type(field, 'example.org')
  await user.click(screen.getByRole('button', { name: ex.saveAddress }))
  expect(await screen.findByText(ex.addressInvalid)).toBeVisible()
  await user.clear(field)
  await user.type(field, '2001:4860:4860::8888')
  await user.click(screen.getByRole('button', { name: ex.saveAddress }))
  expect(await screen.findByText(ex.addressSaved)).toBeVisible()
  expect(address).toBe('2001:4860:4860::8888')
  await act(() => router.navigate('/servers/2?tab=exit'))
  expect(await screen.findByText(ex.needsUpgrade)).toBeVisible()
  expect(screen.queryByRole('combobox', { name: ex.target })).not.toBeInTheDocument()
})

it('repairs a missing destination address without replacing the entry address', async () => {
  let address = ''
  let saved: ExitSettings | undefined
  const fetch = mockAPI({
    '/servers/1/exit': () => Response.json({ ...blank, candidates: [address ? { ...destination, address } :
      { ...destination, address: '', unavailable_reason: 'address_required' }] }),
    'PUT /servers/2/exit-address': (init) => { ({ address } = JSON.parse(String(init.body))); return noContent() },
    'PUT /servers/1/exit': (init) => { saved = JSON.parse(String(init.body)); return noContent() },
  })
  const { user } = mountPage(<ServerExitTab serverID={1} />)
  await user.click(await screen.findByRole('combobox', { name: ex.target }))
  await user.click(await screen.findByRole('option', { name: ex.optionUnavailable(destination.name, ex.reasons.address_required) }))
  expect(screen.getByRole('button', { name: t.common.save })).toBeDisabled()
  const missing = screen.getByRole('textbox', { name: ex.targetAddress(destination.name) })
  await user.type(missing, destination.address)
  await user.click(within(missing.closest('form')!).getByRole('button', { name: ex.saveAddress }))
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(ex.saved)).toBeVisible()
  expect(saved?.exit_server_id).toBe(destination.id)
  expect(fetch.mock.calls.some(([url]) => url.endsWith('/1/exit-address'))).toBe(false)
})

it('shows exit forwarding in the server traffic tab', async () => {
  mockAPI({ '/servers/2/traffic': () => Response.json({
    server_id: 2, billing, user_traffic: { up: 0, down: 0 }, users: [], exit_traffic: { up: 1024, down: 4096 },
  }) })
  mountPage(<ServerDetailPage />, '/servers/2?tab=traffic', '/servers/:id')
  expect(await screen.findByText(ex.forwarded)).toBeVisible()
  expect(screen.getByText('5 KiB')).toBeVisible()
})

it('uses the server IP by default and lets a pinned origin return to following it', async () => {
  const ip = '8.8.4.4'
  let custom = true
  mockAPI({
    '/servers/1/exit': () => Response.json({ ...blank, server_ip: ip, custom_address: custom, address: custom ? blank.address : ip }),
    'PUT /servers/1/exit-address': (init) => {
      expect(JSON.parse(String(init.body))).toEqual({ use_server_ip: true })
      custom = false
      return noContent()
    },
  })
  const { user } = mountPage(<ServerExitTab serverID={1} />)
  await user.click(await screen.findByText(ex.network))
  expect(screen.getByText(ex.customAddress(blank.address))).toBeVisible()
  await user.click(screen.getByRole('button', { name: ex.useServerIP(ip) }))
  expect(await screen.findByText(ex.followAddress(ip))).toBeVisible()
  expect(screen.queryByRole('textbox', { name: ex.address })).not.toBeInTheDocument()
})
