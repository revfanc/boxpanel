import { act, screen, waitFor } from '@testing-library/react'
import { expect, it } from 'vitest'
import { EditInboundPage, NewInboundPage } from './editor'
import { deferred, failure, inbound, mockAPI, mountPage } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('falls back from unavailable Reality suggestions to manual entry, with optional failed probing', async () => {
  const suggestion = deferred<Response>()
  let fail = true
  const fetch = mockAPI({ 'POST /inbounds/suggest': () => suggestion.promise, '/reality/recommend': () => Response.json({ targets: [] }), 'POST /reality/check': () => Response.json({ server: 'manual.example.com', ok: false, tls13: false, h2: false, latency_ms: 0 }), 'POST /inbounds': () => fail ? failure(409) : Response.json(inbound, { status: 201 }) })
  const { user, router } = mountPage(<NewInboundPage />, '/inbounds/new')
  expect(screen.queryByLabelText(t.inbounds.target)).not.toBeInTheDocument()
  await user.click(screen.getByRole('radio', { name: /VLESS/ }))
  expect(screen.getByText(t.inbounds.suggesting)).toBeVisible()
  await act(async () => suggestion.resolve(failure(409, 'reality_target_unavailable')))
  await user.type(await screen.findByLabelText(t.inbounds.target), 'manual.example.com')
  expect(screen.getByText(t.inbounds.manualHint)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.inbounds.recommend }))
  expect(await screen.findByText(t.inbounds.noTargets)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.inbounds.check }))
  await screen.findByText(t.inbounds.checkFailed)
  await user.click(screen.getByRole('button', { name: t.common.create }))
  await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST' && String(init.body).includes('manual.example.com')).length).toBeGreaterThan(1))
  expect(screen.getByLabelText(t.inbounds.target)).toHaveValue('manual.example.com')
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.create }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/inbounds'))
  const request = fetch.mock.calls.filter(([url]) => url.endsWith('/inbounds')).at(-1)?.[1]
  expect(JSON.parse(String(request?.body))).toMatchObject({ protocol: 'vless_reality', options: { server_name: 'manual.example.com', handshake_server: 'manual.example.com' } })
  expect(String(request?.body)).not.toContain('private_key')
  expect(String(request?.body)).not.toContain('public_key')
})
it('prefills Hysteria2, validates advanced options and displays pending creation', async () => {
  const response = deferred<Response>()
  const fetch = mockAPI({ 'POST /inbounds/suggest': () => Response.json({ protocol: 'hysteria2', port: 8443, options: { sni: 'www.bing.com', cert_mode: 'self_signed', up_mbps: 0, down_mbps: 0, port_hopping: { enabled: false, start: 20000, end: 30000 } } }), 'POST /inbounds': () => response.promise })
  const { user } = mountPage(<NewInboundPage />)
  await user.click(screen.getByRole('radio', { name: /Hysteria2/ }))
  expect(await screen.findByLabelText(t.inbounds.port)).toHaveValue(8443)
  await user.click(screen.getByText(t.common.advanced))
  await user.clear(screen.getByLabelText(t.inbounds.end))
  await user.type(screen.getByLabelText(t.inbounds.end), '10000')
  await user.click(screen.getByRole('button', { name: t.common.create }))
  expect(await screen.findByText(t.inbounds.rangeInvalid)).toBeVisible()
  await user.clear(screen.getByLabelText(t.inbounds.end))
  await user.type(screen.getByLabelText(t.inbounds.end), '30000')
  await user.click(screen.getByRole('button', { name: t.common.create }))
  expect(screen.getByRole('button', { name: t.common.saving })).toBeDisabled()
  const body = JSON.parse(String(fetch.mock.calls.at(-1)?.[1]?.body))
  expect(body.options).not.toHaveProperty('obfs_password')
  await act(async () => response.resolve(failure(422)))
  expect(await screen.findByText('Try again')).toBeVisible()
})
it('loads and edits existing nodes, with retry for a missing node and no credential writeback', async () => {
  let missing = true
  const fetch = mockAPI({ 'GET /inbounds/1': () => missing ? failure(404, 'not_found') : Response.json(inbound), 'PUT /inbounds/1': () => Response.json(inbound) })
  const { user, router } = mountPage(<EditInboundPage />, '/inbounds/1', '/inbounds/:id')
  expect(screen.getByText(t.common.loading)).toBeVisible()
  await screen.findByText('Try again')
  missing = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  expect(await screen.findByLabelText(t.inbounds.target)).toHaveValue('example.com')
  await user.clear(screen.getByLabelText(t.inbounds.name))
  await user.type(screen.getByLabelText(t.inbounds.name), 'new remark')
  expect(screen.queryByText(t.common.applyHint)).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/inbounds'))
  const body = JSON.parse(String(fetch.mock.calls.find(([, init]) => init?.method === 'PUT')?.[1]?.body))
  expect(body.remark).toBe('new remark')
  expect(body.options).not.toHaveProperty('public_key')
  expect(body.options).not.toHaveProperty('private_key')
})

it('reminds of interruption only when an edit affects the core or firewall', async () => {
  mockAPI()
  const { user } = mountPage(<EditInboundPage />, '/inbounds/1', '/inbounds/:id')
  await screen.findByLabelText(t.inbounds.port)
  expect(screen.queryByText(t.common.applyHint)).not.toBeInTheDocument()
  await user.clear(screen.getByLabelText(t.inbounds.port))
  await user.type(screen.getByLabelText(t.inbounds.port), '9443')
  expect(screen.getByText(t.common.applyHint)).toBeVisible()
})

it('selects a reachable or offline server for creation and bypasses local Reality checks', async () => {
  const local = { id: 1, name: 'Local', is_local: true, status: 'local' }
  const edge = { id: 2, name: 'Edge', is_local: false, status: 'offline' }
  const pending = { id: 3, name: 'Pending', is_local: false, status: 'pending' }
  const fetch = mockAPI({
    '/servers': () => Response.json([local, edge, pending]),
    'POST /inbounds/suggest': () => Response.json({
      protocol: 'vless_reality', port: 2053,
      options: { server_name: '', handshake_server: '', handshake_port: 443, fingerprint: 'chrome', short_ids: [] },
    }),
    'POST /inbounds': () => Response.json({ ...inbound, server_id: 2, key_status: 'pending' }, { status: 201 }),
  })
  const { user, router } = mountPage(<NewInboundPage />, '/inbounds/new')
  const select = await screen.findByRole('combobox', { name: t.inbounds.server })
  expect(screen.queryByRole('option', { name: /Pending/ })).not.toBeInTheDocument()
  await user.selectOptions(select, '2')
  await user.click(screen.getByRole('radio', { name: /VLESS/ }))
  await user.type(await screen.findByLabelText(t.inbounds.target), 'www.example.com')
  expect(screen.getByText(t.inbounds.remoteCheck)).toBeVisible()
  expect(screen.queryByRole('button', { name: t.inbounds.check })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: t.inbounds.recommend })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: t.common.create }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/inbounds'))
  for (const route of ['/inbounds/suggest', '/inbounds']) {
    const request = fetch.mock.calls.find(([url, init]) => url.endsWith(route) && init?.method === 'POST')
    expect(JSON.parse(String(request?.[1]?.body))).toMatchObject({ server_id: 2 })
  }
})

it('moves an inbound without sending its old Short IDs', async () => {
  const fetch = mockAPI({
    '/servers': () => Response.json([
      { id: 1, name: 'Local', is_local: true, status: 'local' },
      { id: 2, name: 'Edge', is_local: false, status: 'online' },
    ]),
    'PUT /inbounds/1': () => Response.json({ ...inbound, server_id: 2, key_status: 'pending' }),
  })
  const { user } = mountPage(<EditInboundPage />, '/inbounds/1', '/inbounds/:id')
  await user.selectOptions(await screen.findByRole('combobox', { name: t.inbounds.server }), '2')
  expect(screen.getByText(t.inbounds.moveHint)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await waitFor(() => {
    const request = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')
    const body = JSON.parse(String(request?.[1]?.body))
    expect(body.server_id).toBe(2)
    expect(body.options).not.toHaveProperty('short_ids')
  })
})
