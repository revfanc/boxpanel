import { z } from 'zod'
import { zhCN as t } from '@/i18n/zh-CN'

export const scenarioSchema = z.object({ hasDomain: z.boolean(), domain: z.string(), hysteria2: z.boolean() }).superRefine((value, ctx) => {
  const domain = value.domain.trim()
  if (value.hasDomain && (!/^(?=.{1,253}$)(?![\d.]+$)(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(domain))) ctx.addIssue({ code: 'custom', path: ['domain'], message: t.setup.invalidDomain })
})
