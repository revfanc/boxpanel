import { act, cleanup, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { DashboardPage } from '.'
import { billing, edge, failure, mockAPI, mountPage, overview, status } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('shows global user usage and separate metrics, billing and shares for both servers', async () => {
  const sample = { ...billing, supported: true, sampled_at: 1735689600, quota: 1e9 }
  const user = { id: 7, name: 'Usage leader', up: 1024, down: 3072, expire_at: 1735689600, traffic_limit: 4096 }
  const fetch = mockAPI({ '/overview': () => Response.json({
    ...overview, user_traffic: { up: 1024, down: 3072 }, top_users: [user], expiring_users: [user], exhausted_users: [user],
    servers: [
      { ...overview.servers[0], billing: { ...sample, used: 750e6, percent: 75 }, user_traffic: { up: 1024, down: 0 }, share_percent: 25 },
      { ...edge, billing: { ...sample, used: 500e6, percent: 50 }, system: { ...status, server_id: 2, metrics: null },
        user_traffic: { up: 0, down: 3072 }, share_percent: 75 },
    ],
  }) })
  mountPage(<DashboardPage />)
  expect(await screen.findByText(t.dashboard.totalUsage)).toBeVisible()
  expect(screen.getByText('4 KiB', { selector: 'p' })).toBeVisible()
  const local = within(screen.getByRole('link', { name: '本机' }).closest('tr')!)
  expect(local.getByText('12.5%')).toBeVisible()
  expect(local.getByText('1 KiB · 25.0%')).toBeVisible()
  expect(local.getByText(/75\.0%$/, { selector: 'span' }).parentElement).toHaveTextContent('0.75 GB / 1 GB · 75.0%')
  const remote = screen.getByRole('link', { name: edge.name })
  expect(remote).toHaveAttribute('href', '/servers/2')
  const remoteRow = within(remote.closest('tr')!)
  expect(remoteRow.getByText(t.servers.metricsUpgrade)).toBeVisible()
  expect(remoteRow.getByText(t.servers.syncStatuses.synced)).toBeVisible()
  expect(remoteRow.getByText('3 KiB · 75.0%')).toBeVisible()
  expect(remoteRow.getByText(/50\.0%$/, { selector: 'span' }).parentElement).toHaveTextContent('0.5 GB / 1 GB · 50.0%')
  expect(screen.getAllByRole('link', { name: user.name })).toHaveLength(3)
  expect(screen.queryByRole('link', { name: t.pages.newInbound })).not.toBeInTheDocument()
  expect(fetch.mock.calls.every(([url]) => url.endsWith('/overview'))).toBe(true)
})

it('retries an unavailable overview', async () => {
  let fail = true
  mockAPI({ '/overview': () => fail ? failure() : Response.json(overview) })
  const { user } = mountPage(<DashboardPage />)
  await screen.findByText('Try again')
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  expect(await screen.findByText(t.dashboard.totalUsage)).toBeVisible()
})

it('refreshes the aggregate every 15 seconds and stops when unmounted', async () => {
  vi.useFakeTimers()
  const fetch = mockAPI()
  const { client } = mountPage(<DashboardPage />)
  try {
    await act(() => vi.advanceTimersByTimeAsync(10))
    expect(fetch).toHaveBeenCalledTimes(1)
    await act(() => vi.advanceTimersByTimeAsync(15_000))
    expect(fetch).toHaveBeenCalledTimes(2)
    cleanup()
    await act(() => vi.advanceTimersByTimeAsync(15_000))
    expect(fetch).toHaveBeenCalledTimes(2)
  } finally { cleanup(); client.clear(); vi.useRealTimers() }
})
