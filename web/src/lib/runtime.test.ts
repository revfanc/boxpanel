import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
})

describe('runtime paths', () => {
  it('uses the server-injected base for API requests', async () => {
    vi.stubGlobal('window', { __BOXPANEL__: { base: '/k3m9x2p7q1ab', version: 'test' } })
    const { BASE, API_BASE } = await import('./runtime')
    expect(BASE).toBe('/k3m9x2p7q1ab')
    expect(API_BASE).toBe('/k3m9x2p7q1ab/api/v1')
  })

  it('defaults to the root in Vite development mode', async () => {
    vi.stubGlobal('window', {})
    const { BASE, API_BASE } = await import('./runtime')
    expect(BASE).toBe('')
    expect(API_BASE).toBe('/api/v1')
  })
})
