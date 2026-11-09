import { act, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { SettingsPage } from '.'
import { deferred, failure, mockAPI, mountPage, settings } from '@/test/resources'
import { onUnauthorized } from '@/lib/api'
import { zhCN as t } from '@/i18n/zh-CN'
import * as location from '@/lib/location'

it('loads, retries initial errors and validates empty values', async () => {
  const response = deferred<Response>()
  let first = true
  mockAPI({ '/settings': () => first ? response.promise : Response.json(settings) })
  const { user } = mountPage(<SettingsPage />)
  expect(screen.getByText(t.common.loading)).toBeVisible()
  await act(async () => response.resolve(failure()))
  await screen.findByText('Try again')
  first = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  await user.clear(await screen.findByLabelText(t.settings.panelName))
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByText(t.settings.nameInvalid)).toBeVisible()
})
it('saves settings, displays write errors, and confirms destructive address changes before delayed navigation', async () => {
  const visit = vi.spyOn(location, 'visitPanel').mockImplementation(() => {})
  let fail = true
  const fetch = mockAPI({ 'PUT /settings': (init) => fail ? failure(409) : Response.json({ ...settings, ...JSON.parse(String(init.body)), panel_url: 'http://example.com:2053/newpath/', restart_after: 2 }) })
  const { user } = mountPage(<SettingsPage />)
  await screen.findByLabelText(t.settings.panelName)
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await screen.findByText('Try again')
  fail = false
  await user.type(screen.getByLabelText(t.settings.webPath), 'newpath')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(t.settings.confirmHint)
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1)
  await user.click(screen.getByRole('button', { name: t.common.cancel }))
  expect(visit).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  await screen.findByText(t.settings.redirect)
  expect(visit).not.toHaveBeenCalled()
  await waitFor(() => expect(visit).toHaveBeenCalledWith('http://example.com:2053/newpath/'), { timeout: 4000 })
})
it('keeps a wrong current password local and clears caches after a successful account change', async () => {
  const unauthorized = vi.fn()
  const off = onUnauthorized(unauthorized)
  mockAPI({ 'PUT /auth/password': () => failure(401, 'invalid_credentials'), 'PUT /auth/username': () => new Response(null, { status: 204 }) })
  const { user, client, router } = mountPage(<SettingsPage />)
  try {
    await user.type(await screen.findByLabelText(t.settings.oldPassword), 'wrong')
    await user.type(screen.getByLabelText(t.settings.newPassword), 'long-enough-password')
    await user.click(screen.getByRole('button', { name: t.settings.changePassword }))
    await screen.findByText('Try again')
    expect(unauthorized).not.toHaveBeenCalled()
    client.setQueryData(['users', 'links'], { secret: true })
    await user.clear(screen.getByLabelText(t.settings.username))
    await user.type(screen.getByLabelText(t.settings.username), 'newadmin')
    await user.click(screen.getByRole('button', { name: t.settings.changeUsername }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(client.getQueryData(['users', 'links'])).toBeUndefined()
  } finally { off() }
})
