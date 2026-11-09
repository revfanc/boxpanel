import { zhCN as t } from '@/i18n/zh-CN'
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
