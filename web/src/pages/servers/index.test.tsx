import { screen } from '@testing-library/react'
import { expect, it } from 'vitest'
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
