import { act, cleanup, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ServerLogsTab } from './logs'
import { deferred, edge, failure, mockAPI, mountPage, server } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('loads and refreshes local logs without opening a lease', async () => {
  const response = deferred<Response>()
  let first = true
  const fetch = mockAPI({ '/servers/1/logs': () => first ? response.promise : Response.json({ ready: true, lines: ['service started', 'credential [REDACTED]'] }) })
  const { user } = mountPage(<ServerLogsTab server={server} />)
  expect(screen.getByText(t.common.loading)).toBeVisible()
  await act(async () => response.resolve(Response.json({ ready: true, lines: [] })))
  expect(await screen.findByText(t.logs.empty)).toBeVisible()
  first = false
  await user.click(screen.getByRole('button', { name: t.common.refresh }))
  expect(await screen.findByText(/service started/)).toBeVisible()
  expect(fetch.mock.calls.every(([url, init]) => init?.method === 'GET' && url.endsWith('/servers/1/logs'))).toBe(true)
})

it('explains unavailable logs and handles retryable failures', async () => {
  let unavailable = true
  mockAPI({ '/servers/1/logs': () => failure(unavailable ? 409 : 500, unavailable ? 'system_unsupported' : 'internal_error') })
  const { user } = mountPage(<ServerLogsTab server={server} />)
  expect(await screen.findByText(t.logs.unavailable)).toBeVisible()
  unavailable = false
  await user.click(screen.getByRole('button', { name: t.common.refresh }))
  expect(await screen.findByText('Try again')).toBeVisible()
})

it('leases remote logs, polls text every 3 seconds, renews and stops on page leave', async () => {
  vi.useFakeTimers()
  let ready = false
  const fetch = mockAPI({
    '/servers/2/logs': () => Response.json({ ready, lines: ready ? ['remote journal line'] : [] }),
  })
  const { client } = mountPage(<ServerLogsTab server={edge} />)
  try {
    const calls = (method: string) => fetch.mock.calls.filter(([, init]) => init?.method === method)
    await act(() => vi.advanceTimersByTimeAsync(20))
    expect(screen.getByText(t.logs.requesting)).toBeVisible()
    expect(calls('PUT')).toHaveLength(1)
    expect(calls('GET')).toHaveLength(1)
    ready = true
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(screen.getByText('remote journal line')).toBeVisible()
    expect(calls('GET')).toHaveLength(2)
    await act(() => vi.advanceTimersByTimeAsync(12_000))
    expect(calls('PUT')).toHaveLength(2)
    expect(calls('PUT')[1]?.[0]).toBe(calls('PUT')[0]?.[0])
    cleanup()
    const reads = calls('GET').length
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(calls('DELETE')).toHaveLength(1)
    expect(calls('DELETE')[0]?.[0]).toBe(calls('PUT')[0]?.[0])
    expect(calls('DELETE')[0]?.[1]).toHaveProperty('keepalive', true)
    expect(calls('PUT')).toHaveLength(2)
    expect(calls('GET')).toHaveLength(reads)
  } finally { cleanup(); client.clear(); vi.useRealTimers() }
})
