import { act, screen, waitFor, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { UserDetailPage } from './detail'
import { deferred, failure, links, mockAPI, mountPage, proxyUser } from '@/test/resources'
import { zhCN as t, subscriptionText as sub } from '@/i18n/zh-CN'

it('loads user and links, generates a local QR, copies URLs and provides import and single-node links', async () => {
  const response = deferred<Response>()
  mockAPI({ '/users/1/links': () => response.promise })
  const { user } = mountPage(<UserDetailPage />, '/users/1', '/users/:id')
  expect(screen.getByText(t.common.loading)).toBeVisible()
  await screen.findByText('alice')
  await act(async () => response.resolve(Response.json(links)))
  expect(await screen.findByRole('img', { name: sub.qr })).toBeVisible()
  expect(screen.getByRole('link', { name: sub.clashImport })).toHaveAttribute('href', links.import.clash)
  expect(screen.getByRole('link', { name: sub.singboxImport })).toHaveAttribute('href', links.import.singbox)
  expect(screen.getByLabelText('Reality')).toHaveValue(links.nodes[0]!.uri)
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
  await user.click(screen.getByRole('button', { name: `${t.common.copy} ${t.users.auto}` }))
  expect(copy).toHaveBeenCalledWith(links.subscription.auto)
  copy.mockRejectedValueOnce(new Error('denied'))
  await user.click(screen.getByRole('button', { name: `${t.common.copy} ${t.users.base64}` }))
  expect(await screen.findByText(t.common.copyFailed)).toBeVisible()
})
it('handles unavailable certificates, retries to empty nodes, and explains inactive user subscriptions', async () => {
  let unavailable = true
  mockAPI({ '/users/1': () => Response.json({ ...proxyUser, status: 'expired' }), '/users/1/links': () => unavailable ? failure(409, 'subscription_unavailable') : Response.json({ ...links, nodes: [] }) })
  const { user } = mountPage(<UserDetailPage />, '/users/1', '/users/:id')
  await screen.findByText(t.users.unavailable)
  await screen.findByText('Try again')
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  unavailable = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  expect(await screen.findByText(t.users.noNodes)).toBeVisible()
})
it('confirms token reset, retains write failures, removes revoked links and refetches new tokens', async () => {
  let reset = false
  let fail = true
  const response = deferred<Response>()
  const fetch = mockAPI({ '/users/1/links': () => reset ? response.promise.then((result) => result.clone()) : Response.json(links), 'POST /users/1/reset-token': () => { if (fail) return failure(422); reset = true; return Response.json(proxyUser) } })
  const { user, client } = mountPage(<UserDetailPage />, '/users/1', '/users/:id')
  await screen.findByLabelText(t.users.auto)
  await user.click(screen.getByRole('button', { name: t.users.resetToken }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(t.users.tokenHint)
  await user.click(screen.getByRole('button', { name: t.common.cancel }))
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0)
  await user.click(screen.getByRole('button', { name: t.users.resetToken }))
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  expect(await within(screen.getByRole('alertdialog')).findByText('Try again')).toBeVisible()
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  await waitFor(() => expect(screen.queryByLabelText(t.users.auto)).not.toBeInTheDocument())
  expect(client.getQueryData(['users', '1', 'links'])).toBeUndefined()
  const nextLinks = { ...links, subscription: { ...links.subscription, auto: 'https://example.com/sub/newtoken' } }
  await act(async () => response.resolve(Response.json(nextLinks)))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(await screen.findByLabelText(t.users.auto)).toHaveValue(nextLinks.subscription.auto)
})
it('asks for confirmation before credential reset and disable', async () => {
  mockAPI()
  const { user } = mountPage(<UserDetailPage />, '/users/1', '/users/:id')
  await user.click(await screen.findByRole('button', { name: t.users.resetCredentials }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(t.users.credentialsHint)
  await user.click(screen.getByRole('button', { name: t.common.cancel }))
  await user.click(screen.getByRole('button', { name: t.common.disable }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(t.common.disableHint)
})
it('shows a missing user as a recoverable error rather than mounting subscription content', async () => {
  let missing = true
  const fetch = mockAPI({ '/users/1': () => missing ? failure(404, 'not_found') : Response.json(proxyUser) })
  const { user } = mountPage(<UserDetailPage />, '/users/1', '/users/:id')
  await screen.findByText('Try again')
  expect(fetch.mock.calls.some(([url]) => url.endsWith('/links'))).toBe(false)
  missing = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  expect(await screen.findByRole('img', { name: sub.qr })).toBeVisible()
})
