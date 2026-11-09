import { QueryClient, queryOptions } from '@tanstack/react-query'
import { api, APIError, invalidResponse, advanceSession } from '@/lib/api'
import type { Admin, PanelSettings, SetupStatus, SystemStatus } from '@/types/api'

export const queryKeys = {
  auth: ['auth', 'me'] as const,
  setup: ['setup', 'status'] as const,
  settings: ['settings'] as const,
  system: ['system', 'status'] as const,
  inbounds: ['inbounds'] as const,
  users: ['users'] as const,
  rules: ['rules'] as const,
  logs: ['singbox', 'logs'] as const,
}

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        retry: (count, error) => count < 1 && error instanceof APIError && (error.status === 0 || error.status >= 500),
        refetchOnWindowFocus: true,
      },
      mutations: { retry: false },
    },
  })
}

export const sessionQuery = queryOptions({
  queryKey: queryKeys.auth,
  queryFn: async ({ signal }): Promise<Admin | null> => {
    try {
      const value = await api<Admin>('/auth/me', { signal })
      if (!value || typeof value.username !== 'string' || !value.username) throw invalidResponse()
      return value
    } catch (error) {
      if (error instanceof APIError && error.status === 401) return null
      throw error
    }
  },
  retry: false,
})

export const setupQuery = queryOptions({
  queryKey: queryKeys.setup,
  queryFn: async ({ signal }): Promise<SetupStatus | null> => {
    try {
      const value = await api<SetupStatus>('/setup/status', { signal })
      if (!value || typeof value.completed !== 'boolean' || typeof value.step !== 'string') throw invalidResponse()
      return value
    } catch (error) {
      // Only the explicitly unimplemented endpoint is compatible. Other failures block.
      if (error instanceof APIError && error.status === 404 && error.code === 'not_found') return null
      throw error
    }
  },
  retry: false,
})

export const settingsQuery = queryOptions({
  queryKey: queryKeys.settings,
  queryFn: ({ signal }) => api<PanelSettings>('/settings', { signal }),
})
export const systemQuery = queryOptions({
  queryKey: queryKeys.system,
  queryFn: ({ signal }) => api<SystemStatus>('/system/status', { signal }),
})

export function replaceSession(client: QueryClient, admin: Admin | null) {
  advanceSession()
  void client.cancelQueries()
  // Keep the observed session query attached, including when already on /login.
  // Removing it here strands its mounted observer on a cancelled pending query.
  client.removeQueries({ predicate: (query) => query.queryKey[0] !== 'auth' })
  client.getMutationCache().clear()
  client.setQueryData(queryKeys.auth, admin)
}
