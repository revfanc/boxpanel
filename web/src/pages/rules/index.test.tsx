import { act, screen, waitFor, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { RulesPage } from '.'
import { deferred, failure, mockAPI, mountPage, rules } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('loads rules, retries errors and starts with empty domain tags', async () => {
  const response = deferred<Response>()
  let first = true
  mockAPI({ '/rules': () => first ? response.promise : Response.json(rules) })
  const { user } = mountPage(<RulesPage />)
  expect(screen.getByText(t.common.loading)).toBeVisible()
  await act(async () => response.resolve(failure()))
  await screen.findByText('Try again')
  first = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  expect((await screen.findAllByText(t.rules.noDomains))).toHaveLength(2)
  expect(screen.getByRole('radio', { name: new RegExp(t.rules.bypass_cn) })).toBeChecked()
})
it('validates domains/DNS, saves all options including an uncommitted tag, and preserves input on failed writes', async () => {
  const response = deferred<Response>()
  let saved = rules
  let fail = true
  const fetch = mockAPI({ 'GET /rules': () => Response.json(saved), 'PUT /rules': (init) => { if (fail) return response.promise; saved = JSON.parse(String(init.body)); return Response.json(saved) } })
  const { user } = mountPage(<RulesPage />)
  await user.type(await screen.findByLabelText(t.rules.direct, { selector: 'input' }), '*.bad.com{Enter}')
  expect(await screen.findByText(t.rules.domainInvalid)).toBeVisible()
  await user.clear(screen.getByLabelText(t.rules.direct, { selector: 'input' }))
  await user.type(screen.getByLabelText(t.rules.direct, { selector: 'input' }), 'example.cn{Enter}')
  await user.type(screen.getByLabelText(t.rules.proxy, { selector: 'input' }), 'example.com')
  await user.click(screen.getByRole('radio', { name: new RegExp(t.rules.gfw_only) }))
  await user.click(screen.getByRole('switch', { name: t.rules.ads }))
  await user.click(screen.getByRole('switch', { name: t.rules.fakeip }))
  await user.click(screen.getByRole('switch', { name: t.rules.ipv6 }))
  await user.selectOptions(screen.getByLabelText(t.rules.source), 'sagernet')
  await user.clear(screen.getByLabelText(t.rules.remote))
  await user.type(screen.getByLabelText(t.rules.remote), 'http://invalid.example/dns')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await screen.findByText(t.rules.httpsInvalid)
  await user.clear(screen.getByLabelText(t.rules.remote))
  await user.type(screen.getByLabelText(t.rules.remote), 'https://1.1.1.1/dns-query')
  await user.type(screen.getByLabelText(t.rules.mirror), 'https://mirror.example/')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(screen.getByRole('button', { name: t.common.saving })).toBeDisabled()
  await act(async () => response.resolve(failure(400)))
  await screen.findByText('Try again')
  expect(screen.getByLabelText(t.rules.proxy, { selector: 'input' })).toHaveValue('example.com')
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(t.common.saved)).toBeVisible()
  const body = JSON.parse(String(fetch.mock.calls.filter(([, init]) => init?.method === 'PUT').at(-1)?.[1]?.body))
  expect(body).toMatchObject({ preset: 'gfw_only', custom_direct: ['example.cn'], custom_proxy: ['example.com'], block_ads: true, dns: { fakeip: true }, ipv6: true, source: 'sagernet', mirror: 'https://mirror.example/' })
  expect(body).not.toHaveProperty('direct_draft')
  expect(body).not.toHaveProperty('proxy_draft')
  await user.click(screen.getByRole('button', { name: t.common.remove('example.cn') }))
  expect(screen.queryByRole('button', { name: t.common.remove('example.cn') })).not.toBeInTheDocument()
})
it('loads previews only on demand, handles no users and errors, switches JSON/YAML and restores focus', async () => {
  let noUser = true
  let fail = true
  const fetch = mockAPI({ '/rules/preview?format=singbox': () => noUser ? failure(404, 'not_found') : Response.json({ format: 'singbox', config: { password: '[REDACTED]' } }), '/rules/preview?format=clash': () => fail ? failure(409, 'subscription_unavailable') : Response.json({ format: 'clash', config: 'password: [REDACTED]' }) })
  const { user } = mountPage(<RulesPage />)
  await screen.findByLabelText(t.rules.remote)
  expect(fetch.mock.calls.some(([url]) => url.includes('/preview'))).toBe(false)
  const trigger = screen.getByRole('button', { name: t.rules.preview })
  await user.click(trigger)
  expect(await within(screen.getByRole('dialog')).findByText(t.rules.noPreview)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.common.close }))
  await waitFor(() => expect(trigger).toHaveFocus())
  noUser = false
  await user.click(trigger)
  expect(await screen.findByText(/"password": "\[REDACTED\]"/)).toBeVisible()
  await user.selectOptions(screen.getByLabelText(t.rules.format), 'clash')
  await within(screen.getByRole('dialog')).findByText('Try again')
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  expect(await screen.findByText('password: [REDACTED]')).toBeVisible()
  await user.keyboard('{Escape}')
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
})
