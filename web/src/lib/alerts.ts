import { queryOptions } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { AlertSettings, ServerAlertSettings } from '@/types/api'

export function alertSettingsFor(serverID: number) {
  return queryOptions({
    queryKey: ['servers', serverID, 'alerts'],
    queryFn: ({ signal }) => api<ServerAlertSettings>(`/servers/${serverID}/alerts`, { signal }),
  })
}

export const alertSettingsQuery = queryOptions({
  queryKey: ['alerts', 'settings'],
  queryFn: ({ signal }) => api<AlertSettings>('/alerts/settings', { signal }),
})

export function utcDate(seconds: number) {
  return new Date(seconds * 1000).toISOString().slice(0, 19).replace('T', ' ') + ' UTC'
}

export function billingGB(bytes: number) {
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 3 }).format(bytes / 1e9) + ' GB'
}
