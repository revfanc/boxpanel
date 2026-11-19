import { screen, waitFor, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ServersPage } from '.'
import { zhCN as t } from '@/i18n/zh-CN'
import { failure, mockAPI, mountPage, server } from '@/test/resources'

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
  expect(screen.queryByRole('textbox', { name: t.servers.command })).not.toBeInTheDocument()
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(true)
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

it('shows remote sync progress and updates the subscription host', async () => {
  const edge = {
    ...server, id: 2, name: 'Edge', is_local: false, status: 'online', public_host: 'edge.example.com',
    desired_generation: 7, applied_generation: 6, sync_status: 'failed', sync_error: 'port_in_use', singbox_active: true,
  }
  const fetch = mockAPI({
    '/servers': () => Response.json([server, edge]),
    'PUT /servers/2': (init) => Response.json({ ...edge, ...JSON.parse(String(init.body)) }),
  })
  const { user } = mountPage(<ServersPage />)
  expect(await screen.findByText(t.servers.syncStatuses.failed)).toBeVisible()
  expect(screen.getByText('6 / 7')).toBeVisible()
  expect(screen.getByText(t.servers.syncErrors.port_in_use)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.common.edit }))
  const input = screen.getByRole('textbox', { name: t.servers.address })
  await user.clear(input)
  await user.type(input, 'new.edge.example.com')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText('new.edge.example.com')).toBeVisible()
  const request = fetch.mock.calls.find(([, init]) => init?.method === 'PUT')
  expect(JSON.parse(String(request?.[1]?.body))).toMatchObject({ public_host: 'new.edge.example.com' })
})
