import { z } from 'zod'
import { hostSchema } from '@/schemas/settings'
import type { RulesSettings } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export const domainSchema = z.string().refine((value) => hostSchema.safeParse(value).success && !value.includes(':') && !/^\d+\.\d+\.\d+\.\d+$/.test(value), t.rules.domainInvalid)
const https = (value: string) => {
  try { const url = new URL(value); return value.startsWith('https://') && !!url.hostname && !url.username && !url.password && !url.hash && !/[\s\\]/.test(value) && url.port !== '0' } catch { return false }
}
function interval(value: string) {
  if (/^\d+d$/.test(value)) return Number(value.slice(0, -1)) >= 1 && Number(value.slice(0, -1)) <= 30
  if (!/^(?:(?:\d+(?:\.\d*)?|\.\d+)(?:h|ms|m|s|us|µs|μs|ns))+$/.test(value)) return false
  const scale: Record<string, number> = { h: 3600, m: 60, s: 1, ms: .001, us: .000001, 'µs': .000001, 'μs': .000001, ns: .000000001 }
  const seconds = [...value.matchAll(/(\d+(?:\.\d*)?|\.\d+)(h|ms|m|s|us|µs|μs|ns)/g)].reduce((sum, part) => sum + Number(part[1]) * scale[part[2]!]!, 0)
  return Number.isInteger(seconds) && seconds >= 3600 && seconds <= 30 * 86400
}
export const rulesSchema = z.object({
  preset: z.enum(['bypass_cn', 'gfw_only', 'global']), block_ads: z.boolean(),
  custom_direct: z.array(domainSchema).max(128, t.rules.domainInvalid), custom_proxy: z.array(domainSchema).max(128, t.rules.domainInvalid),
  direct_draft: z.string().refine((value) => !value.trim() || domainSchema.safeParse(value.trim()).success, t.rules.domainInvalid),
  proxy_draft: z.string().refine((value) => !value.trim() || domainSchema.safeParse(value.trim()).success, t.rules.domainInvalid),
  source: z.enum(['metacubex', 'sagernet']), mirror: z.string().refine((value) => !value || https(value) && value.endsWith('/') && !value.includes('?'), t.rules.mirrorInvalid),
  update_interval: z.string().refine(interval, t.rules.intervalInvalid),
  dns: z.object({ remote: z.string().refine(https, t.rules.httpsInvalid), local: z.string().refine(https, t.rules.httpsInvalid), fakeip: z.boolean() }), ipv6: z.boolean(),
}).superRefine((value, ctx) => {
  for (const [list, draft] of [['custom_direct', 'direct_draft'], ['custom_proxy', 'proxy_draft']] as const) {
    if (value[draft].trim() && !value[list].includes(value[draft].trim().toLowerCase()) && value[list].length >= 128) ctx.addIssue({ code: 'custom', path: [draft], message: t.rules.domainInvalid })
  }
})
export type RulesFormValues = z.infer<typeof rulesSchema>
export function rulesBody(value: RulesFormValues): RulesSettings {
  const { direct_draft, proxy_draft, ...settings } = value
  const merge = (values: string[], draft: string) => [...new Set([...values, ...(draft.trim() ? [draft.trim().toLowerCase()] : [])])]
  return { ...settings, custom_direct: merge(value.custom_direct, direct_draft), custom_proxy: merge(value.custom_proxy, proxy_draft) }
}
