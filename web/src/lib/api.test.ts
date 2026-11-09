import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError, advanceSession, onUnauthorized } from './api'
import { zhCN } from '@/i18n/zh-CN'

const errorResponse = (status = 401) => Response.json({ error: { code: 'unauthorized', message: 'Denied', hint: 'Sign in' } }, { status })
afterEach(() => advanceSession())

describe('API transport', () => {
  it('uses the runtime prefix, same-origin cookies, no-store, JSON and a signal', async () => {
    vi.resetModules()
    window.__BOXPANEL__ = { base: '/randompath', version: 'test' }
    try {
      const { api: prefixedAPI } = await import('./api')
      const fetcher = vi.fn().mockResolvedValue(Response.json({ username: 'admin' }))
      vi.stubGlobal('fetch', fetcher)
      const signal = new AbortController().signal
      await prefixedAPI('/auth/login', { method: 'POST', body: { username: 'admin', password: 'test' }, signal })
      expect(fetcher).toHaveBeenCalledWith('/randompath/api/v1/auth/login', expect.objectContaining({
        method: 'POST', credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal,
        headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: 'admin', password: 'test' }),
      }))
    } finally { delete window.__BOXPANEL__; vi.resetModules() }
  })

  it('supports raw arrays and 204 responses without JSON bodies', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(Response.json([])).mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetcher)
    expect(await api('/users')).toEqual([])
    expect(await api('/auth/logout', { method: 'POST' })).toBeUndefined()
    expect(fetcher.mock.calls[1]?.[1]).toMatchObject({ body: undefined, headers: { Accept: 'application/json' } })
  })

  it('preserves structured errors and optional redacted diagnostics', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ error: { code: 'singbox_check_failed', message: 'Check failed', hint: 'Review input', detail: 'Redacted output' } }, { status: 422 })))
    await expect(api('/inbounds', { method: 'POST', body: {} })).rejects.toMatchObject({ status: 422, code: 'singbox_check_failed', message: 'Check failed', hint: 'Review input', detail: 'Redacted output' })
  })

  it('supplies next steps for network failures, malformed errors and invalid JSON', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValueOnce(new TypeError('private network diagnostics'))
      .mockResolvedValueOnce(new Response('<html>internal secrets</html>', { status: 502 }))
      .mockResolvedValueOnce(new Response('not-json')))
    await expect(api('/users')).rejects.toMatchObject({ code: 'network_error', hint: zhCN.errors.networkHint })
    await expect(api('/users')).rejects.toMatchObject({ status: 502, message: zhCN.errors.request, hint: zhCN.errors.retryHint })
    await expect(api('/users')).rejects.toMatchObject({ code: 'invalid_response', hint: zhCN.errors.responseHint })
  })

  it('keeps cancellation distinguishable from network errors', async () => {
    const controller = new AbortController()
    const abort = new DOMException('Aborted', 'AbortError')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(abort))
    controller.abort()
    await expect(api('/users', { signal: controller.signal })).rejects.toBe(abort)
  })

  it('notifies once for concurrent 401s, but never for invalid login credentials', async () => {
    const listener = vi.fn()
    const dispose = onUnauthorized(listener)
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => errorResponse()))
    try {
      await expect(api('/auth/login', { method: 'POST', body: {} })).rejects.toBeInstanceOf(APIError)
      expect(listener).not.toHaveBeenCalled()
      await Promise.allSettled([api('/users'), api('/settings')])
      expect(listener).toHaveBeenCalledTimes(1)
    } finally { dispose() }
  })

  it('ignores a late unauthorized response after a new login', async () => {
    const listener = vi.fn()
    const dispose = onUnauthorized(listener)
    let resolve!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((done) => { resolve = done })))
    const request = api('/users').catch((error: unknown) => error)
    advanceSession()
    resolve(errorResponse())
    try { await request; expect(listener).not.toHaveBeenCalled() } finally { dispose() }
  })

  it.each(['//evil.example', '/../users', '/%2e%2e/users', '/users\\other', '/users#fragment'] as const)('rejects an invalid API path %s before sending', async (path) => {
    vi.stubGlobal('fetch', vi.fn())
    await expect(api(path)).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).not.toHaveBeenCalled()
  })
})
