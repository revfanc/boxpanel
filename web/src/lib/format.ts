import { zhCN as t } from '@/i18n/zh-CN'
import type { ServerMetrics } from '@/types/api'
export function bytes(value: number) {
  if (!Number.isFinite(value) || value < 0) return t.common.unknown
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  const index = Math.min(units.length - 1, Math.max(0, Math.floor(Math.log2(value || 1) / 10)))
  return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 }).format(value / 1024 ** index)} ${units[index]}`
}
export function duration(seconds: number) {
  const hours = Math.floor(seconds / 3600)
  return hours >= 24 ? t.dashboard.days(Math.floor(hours / 24), hours % 24) : t.dashboard.minutes(hours, Math.floor(seconds % 3600 / 60))
}
export function expiry(seconds: number) { return seconds === 0 ? t.common.never : new Date(seconds * 1000).toLocaleString('zh-CN') }

const metricNumber = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 1 })
function metricSize(mb: number) {
  return mb >= 1000 ? `${metricNumber.format(mb / 1000)} GB` : `${metricNumber.format(mb)} MB`
}
export function metricUsage(usage: ServerMetrics['mem']) {
  return t.dashboard.memoryUsage(metricSize(usage.used_mb), metricSize(usage.total_mb))
}
export function metricRates(net: ServerMetrics['net']) {
  return t.dashboard.rates(bytes(net.rx_rate), bytes(net.tx_rate))
}

const relativeFormatter = new Intl.RelativeTimeFormat('zh-CN')
export function relativeTime(timestamp: number, now: number) {
  const seconds = Math.max(0, Math.floor(now / 1000) - timestamp)
  if (seconds < 60) return t.servers.secondsAgo(seconds)
  if (seconds < 3600) return relativeFormatter.format(-Math.floor(seconds / 60), 'minute')
  if (seconds < 86400) return relativeFormatter.format(-Math.floor(seconds / 3600), 'hour')
  return relativeFormatter.format(-Math.floor(seconds / 86400), 'day')
}
