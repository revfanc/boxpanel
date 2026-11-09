import { act, screen, waitFor, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { UsersPage } from '.'
import { deferred, failure, mockAPI, mountPage, proxyUser } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('loads and recovers to an empty state, then creates a user with bytes and Unix seconds', async () => {
  const response = deferred<Response>()
  let first = true
  let users = [] as typeof proxyUser[]
  const fetch = mockAPI({ 'GET /users': () => first ? response.promise : Response.json(users), 'POST /users': (init) => { users = [{ ...proxyUser, ...JSON.parse(String(init.body)) }]; return Response.json(users[0], { status: 201 }) } })
  const { user } = mountPage(<UsersPage />)
  expect(screen.getByText(t.common.loading)).toBeVisible()
  await act(async () => response.resolve(failure()))
  await screen.findByText('Try again')
  first = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  await screen.findByText(t.users.empty)
  await user.click(screen.getByRole('button', { name: t.users.create }))
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: t.common.create }))
  await screen.findByText(t.login.usernameInvalid)
  await user.type(screen.getByLabelText(t.users.name), 'alice')
  await user.clear(screen.getByLabelText(t.users.limit))
  await user.type(screen.getByLabelText(t.users.limit), '1.5')
  await user.click(screen.getByRole('button', { name: t.common.create }))
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  expect(screen.getByText('alice')).toBeVisible()
  expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuemax', '100')
  const body = JSON.parse(String(fetch.mock.calls.find(([, init]) => init?.method === 'POST')?.[1]?.body))
  expect(body).toEqual({ name: 'alice', remark: '', traffic_limit: 1610612736, expire_at: 0 })
})
it('edits with pending/error states and confirms user deletion before invalidating the list', async () => {
  let users = [proxyUser]
  const write = deferred<Response>()
  let failDelete = true
  const fetch = mockAPI({ '/users': () => Response.json(users), 'PUT /users/1': () => write.promise, 'DELETE /users/1': () => { if (failDelete) return failure(422); users = []; return new Response(null, { status: 204 }) } })
  const { user } = mountPage(<UsersPage />)
  await user.click(await screen.findByRole('button', { name: t.common.edit }))
  expect(screen.getByLabelText(t.users.name)).toHaveValue('alice')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(screen.getByRole('button', { name: t.common.saving })).toBeDisabled()
  await act(async () => write.resolve(failure(409, 'name_exists')))
  expect(await within(screen.getByRole('dialog')).findByText('Try again')).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.common.cancel }))
  await user.click(screen.getByRole('button', { name: t.common.delete }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(t.users.deleteHint)
  await user.click(screen.getByRole('button', { name: t.common.cancel }))
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'DELETE')).toHaveLength(0)
  await user.click(screen.getByRole('button', { name: t.common.delete }))
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  await within(screen.getByRole('alertdialog')).findByText('Try again')
  failDelete = false
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  expect(await screen.findByText(t.users.empty)).toBeVisible()
})
