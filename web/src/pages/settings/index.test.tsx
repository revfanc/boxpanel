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

it.each(['acme', 'self_signed', 'http'] as const)('shows the saved host address with copy and %s navigation behavior', async (mode) => {
  const visit = vi.spyOn(location, 'visitPanel').mockImplementation(() => {})
  let saved = { ...settings, panel_tls_mode: mode, web_path: 'randompath' }
  const panelURL = `${mode === 'http' ? 'http' : 'https'}://new.example.com:2053/randompath/`
  mockAPI({
    'GET /settings': () => Response.json(saved),
    'PUT /settings': (init) => { saved = { ...saved, ...JSON.parse(String(init.body)) }; return Response.json({ ...saved, panel_url: panelURL, restart_after: 0 }) },
  })
  const { user } = mountPage(<SettingsPage />)
  const clipboard = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  await user.clear(await screen.findByLabelText(t.settings.publicHost))
  await user.type(screen.getByLabelText(t.settings.publicHost), 'new.example.com')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  expect(await screen.findByRole('dialog')).toHaveTextContent(t.addressChange.title)
  expect(screen.getByLabelText(t.addressChange.url)).toHaveValue(panelURL)
  expect(visit).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: `${t.common.copy} ${t.addressChange.url}` }))
  expect(clipboard).toHaveBeenCalledWith(panelURL)
  if (mode === 'http') {
    expect(screen.getByText(t.addressChange.dev)).toBeVisible()
    expect(screen.queryByRole('button', { name: t.addressChange.go })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: t.addressChange.continue }))
    expect(visit).not.toHaveBeenCalled()
  } else {
    expect(screen.getByText(t.addressChange.hint)).toBeVisible()
    await user.click(screen.getByRole('button', { name: t.addressChange.go }))
    expect(visit).toHaveBeenCalledWith(panelURL)
  }
})

it('does not prompt on failed, unchanged, or already-current host saves', async () => {
  let fail = true
  let panelURL = 'https://new.example.com:2053/'
  mockAPI({ 'PUT /settings': (init) => fail ? failure() : Response.json({ ...settings, ...JSON.parse(String(init.body)), panel_url: panelURL, restart_after: 0 }) })
  const { user } = mountPage(<SettingsPage />)
  await screen.findByLabelText(t.settings.publicHost)
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await screen.findByText('Try again')
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await screen.findByText(t.common.saved)
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  panelURL = `${window.location.origin}/`
  await user.clear(screen.getByLabelText(t.settings.publicHost))
  await user.type(screen.getByLabelText(t.settings.publicHost), window.location.hostname)
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await screen.findByText(t.common.saved)
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})

it('keeps a combined host and listener change dialog open until the user chooses to navigate', async () => {
  const visit = vi.spyOn(location, 'visitPanel').mockImplementation(() => {})
  const panelURL = 'https://new.example.com:2443/randompath/'
  mockAPI({ 'PUT /settings': (init) => Response.json({ ...settings, ...JSON.parse(String(init.body)), panel_tls_mode: 'acme', panel_url: panelURL, restart_after: 2 }) })
  const { user } = mountPage(<SettingsPage />)
  await user.clear(await screen.findByLabelText(t.settings.publicHost))
  await user.type(screen.getByLabelText(t.settings.publicHost), 'new.example.com')
  await user.type(screen.getByLabelText(t.settings.webPath), 'randompath')
  await user.click(screen.getByRole('button', { name: t.common.save }))
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  const go = await screen.findByRole('button', { name: t.addressChange.go })
  expect(go).toBeDisabled()
  await waitFor(() => expect(go).toBeEnabled(), { timeout: 4000 })
  expect(visit).not.toHaveBeenCalled()
  await user.click(go)
  expect(visit).toHaveBeenCalledWith(panelURL)
})
