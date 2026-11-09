import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { api, APIError } from '@/lib/api'
import { queryKeys } from '@/lib/query'
import type { Inbound, User, RulesSettings, SystemCheck } from '@/types/api'

export const inboundsQuery = queryOptions({ queryKey: queryKeys.inbounds, queryFn: ({ signal }) => api<Inbound[]>('/inbounds', { signal }) })
export const usersQuery = queryOptions({ queryKey: queryKeys.users, queryFn: ({ signal }) => api<User[]>('/users', { signal }) })
export const rulesQuery = queryOptions({ queryKey: queryKeys.rules, queryFn: ({ signal }) => api<RulesSettings>('/rules', { signal }) })
export const inboundQuery = (id: string) => queryOptions({ queryKey: [...queryKeys.inbounds, id], queryFn: ({ signal }) => api<Inbound>(`/inbounds/${id}`, { signal }) })
export const userQuery = (id: string) => queryOptions({ queryKey: [...queryKeys.users, id], queryFn: ({ signal }) => api<User>(`/users/${id}`, { signal }) })
export const unsupported = (error: unknown) => error instanceof APIError && error.status === 409 && error.code === 'system_unsupported'

export async function refreshResources(client: QueryClient) {
  // Cancel old snapshots so an in-flight links/preview read cannot restore a
  // revoked token or a deleted node after a successful write.
  const roots = ['inbounds', 'users', 'rules', 'system', 'settings', 'singbox']
  const filter = { predicate: (query: { queryKey: readonly unknown[] }) => roots.includes(String(query.queryKey[0])) }
  await client.cancelQueries(filter)
  await client.invalidateQueries(filter)
}

export const checkQuery = queryOptions({ queryKey: ['system', 'check'], queryFn: ({ signal }) => api<SystemCheck>('/system/check', { signal }), retry: false })
