import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { DashboardPage } from '.'
import { deferred, failure, inbound, mockAPI, mountPage, proxyUser, status } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('loads, shows empty resources and handles unsupported metrics without zero-value cards', async () => {
  const response = deferred<Response>()
  mockAPI({ '/system/status': () => response.promise })
  mountPage(<DashboardPage />)
  expect(screen.getAllByText(t.common.loading).length).toBeGreaterThan(0)
  await screen.findByText(t.dashboard.empty)
  await act(async () => response.resolve(Response.json({ ...status, supported: false })))
  expect(await screen.findByText(t.common.devHint)).toBeVisible()
  expect(screen.queryByText(t.dashboard.cpu)).not.toBeInTheDocument()
})
it('shows live cards, retries errors and confirms a restart with pending and failure states', async () => {
  let fail = true
  const restart = deferred<Response>()
  const fetch = mockAPI({ '/inbounds': () => fail ? failure() : Response.json([inbound]), '/users': () => Response.json([proxyUser]), 'POST /singbox/restart': () => restart.promise })
  const { user } = mountPage(<DashboardPage />)
  expect(await screen.findByText('12.5%')).toBeVisible()
  await screen.findByText('Try again')
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
  await user.click(screen.getByRole('button', { name: t.dashboard.restart }))
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: t.common.cancel }))
  await user.click(screen.getByRole('button', { name: t.dashboard.restart }))
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  expect(screen.getByRole('button', { name: t.common.working })).toBeDisabled()
  await act(async () => restart.resolve(failure(422, 'singbox_check_failed')))
  expect(await within(screen.getByRole('alertdialog')).findByText('Try again')).toBeVisible()
})
it('refreshes all overview resources after five seconds and stops polling on unmount', async () => {
  vi.useFakeTimers()
  const fetch = mockAPI()
  const { client } = mountPage(<DashboardPage />)
  try {
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/system/status'))).toHaveLength(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    for (const path of ['/system/status', '/inbounds', '/users']) expect(fetch.mock.calls.filter(([url]) => url.endsWith(path))).toHaveLength(2)
    cleanup()
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/system/status'))).toHaveLength(2)
  } finally { cleanup(); client.clear(); vi.useRealTimers() }
})
