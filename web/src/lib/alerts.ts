import { queryOptions } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { AlertOverview, AlertSettings } from '@/types/api'

export function alertSettingsFor(serverID?: number) {
  return queryOptions({
    queryKey: serverID === undefined ? ['alerts', 'settings'] : ['alerts', 'settings', serverID],
    queryFn: ({ signal }) => api<AlertSettings>(`/alerts/settings${serverID === undefined ? '' : `?server_id=${serverID}`}`, { signal }),
  })
}

export const alertSettingsQuery = alertSettingsFor()

export const alertOverviewQuery = queryOptions({
  queryKey: ['alerts', 'overview'],
  queryFn: ({ signal }) => api<AlertOverview>('/alerts', { signal }),
  refetchInterval: 15000,
})

export function utcDate(seconds: number) {
  return new Date(seconds * 1000).toISOString().slice(0, 19).replace('T', ' ') + ' UTC'
}

export function billingGB(bytes: number) {
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 3 }).format(bytes / 1e9) + ' GB'
}
