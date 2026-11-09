import { act, cleanup, fireEvent, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { LogsPage } from '.'
import { deferred, failure, mockAPI, mountPage } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'
it('loads, displays an empty log and refreshes successful log text', async () => {
  const response = deferred<Response>()
  let first = true
  mockAPI({ '/singbox/logs?lines=200': () => first ? response.promise : Response.json({ lines: ['service started', 'credential [REDACTED]'] }) })
  const { user } = mountPage(<LogsPage />)
  expect(screen.getAllByText(t.common.loading).length).toBeGreaterThan(0)
  await act(async () => response.resolve(Response.json({ lines: [] })))
  await screen.findByText(t.logs.empty)
  first = false
  await user.click(screen.getByRole('button', { name: t.common.refresh }))
  expect(await screen.findByText(/service started/)).toBeVisible()
  await user.click(screen.getByRole('switch', { name: t.logs.auto }))
  expect(screen.getByRole('switch')).not.toBeChecked()
})
it('renders 409 as an unavailable notice and other failures with retry advice', async () => {
  let unavailable = true
  mockAPI({ '/singbox/logs?lines=200': () => failure(unavailable ? 409 : 500, unavailable ? 'system_unsupported' : 'internal_error') })
  const { user } = mountPage(<LogsPage />)
  await screen.findByText(t.logs.unavailable)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  unavailable = false
  await user.click(screen.getByRole('button', { name: t.common.refresh }))
  expect(await screen.findByText('Try again')).toBeVisible()
})
it('pauses timed refresh when auto-refresh is disabled', async () => {
  vi.useFakeTimers()
  const fetch = mockAPI()
  const { client } = mountPage(<LogsPage />)
  try {
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(fetch).toHaveBeenCalledTimes(2)
    fireEvent.click(screen.getByRole('switch', { name: t.logs.auto }))
    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    expect(fetch).toHaveBeenCalledTimes(2)
  } finally { cleanup(); client.clear(); vi.useRealTimers() }
})
