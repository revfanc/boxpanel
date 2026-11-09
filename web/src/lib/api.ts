import { API_BASE } from '@/lib/runtime'
import { zhCN } from '@/i18n/zh-CN'
import type { APIErrorBody } from '@/types/api'

export class APIError extends Error {
  readonly status: number
  readonly code: string
  readonly hint: string
  readonly detail?: string
  readonly retryAfter?: number

  constructor(status: number, error: APIErrorBody, retryAfter?: number) {
    super(error.message)
    this.name = 'APIError'
    this.status = status
    this.code = error.code
    this.hint = error.hint
    this.detail = error.detail
    this.retryAfter = retryAfter
  }
}

const unauthorizedListeners = new Set<() => void>()
let sessionGeneration = 0

export function onUnauthorized(listener: () => void) {
  unauthorizedListeners.add(listener)
  return () => { unauthorizedListeners.delete(listener) }
}

// Ignore late 401 responses from requests belonging to a previous session.
export function advanceSession() { sessionGeneration += 1 }

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  signal?: AbortSignal
}

function errorBody(value: unknown): APIErrorBody | undefined {
  if (!value || typeof value !== 'object' || !('error' in value)) return
  const error = value.error
  if (!error || typeof error !== 'object') return
  if (!('code' in error) || typeof error.code !== 'string' ||
      !('message' in error) || typeof error.message !== 'string') return
  return {
    code: error.code,
    message: error.message || zhCN.errors.request,
    hint: 'hint' in error && typeof error.hint === 'string' && error.hint ? error.hint : zhCN.errors.retryHint,
    detail: 'detail' in error && typeof error.detail === 'string' ? error.detail : undefined,
  }
}

export function invalidResponse() {
  return new APIError(0, { code: 'invalid_response', message: zhCN.errors.response, hint: zhCN.errors.responseHint })
}

export async function api<T>(path: `/${string}`, options: RequestOptions = {}): Promise<T> {
  const generation = sessionGeneration
  // Only API-relative paths are accepted; requests must stay inside the panel prefix.
  if (path.startsWith('//') || path.includes('\\') || path.includes('#') || Array.from(path).some((char) => char.charCodeAt(0) <= 32) || path.split('?')[0]!.split('/').some((part) => part === '.' || part === '..' || part.includes('%'))) {
    throw invalidResponse()
  }
  let response: Response
  try {
    response = await fetch(`${API_BASE}${path}`, {
      method: options.method ?? 'GET',
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'error',
      headers: { Accept: 'application/json', ...(options.body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal,
    })
  } catch (error) {
    if (options.signal?.aborted || error instanceof DOMException && error.name === 'AbortError') throw error
    throw new APIError(0, { code: 'network_error', message: zhCN.errors.network, hint: zhCN.errors.networkHint })
  }
  if (response.status === 401 && path !== '/auth/login' && generation === sessionGeneration) {
    advanceSession()
    unauthorizedListeners.forEach((listener) => listener())
  }
  if (response.status === 204 && response.ok) return undefined as T
  let value: unknown
  try {
    // Never expose an HTML proxy error page or raw response in the UI.
    value = await response.json()
  } catch {
    if (response.ok) throw invalidResponse()
  }
  if (!response.ok) {
    const retryHeader = response.headers.get('Retry-After')
    const seconds = retryHeader && /^\d+$/.test(retryHeader) ? Number(retryHeader) : undefined
    throw new APIError(response.status, errorBody(value) ?? {
      code: 'http_error', message: zhCN.errors.request, hint: zhCN.errors.retryHint,
    }, seconds && Number.isSafeInteger(seconds) ? seconds : undefined)
  }
  return value as T
}
