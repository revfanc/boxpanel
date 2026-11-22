import { z } from 'zod'
import { nameSchema, remarkSchema } from '@/schemas/settings'
import type { User, UserInput } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'
const GiB = 1024 ** 3
export const userSchema = z.object({
  name: nameSchema, remark: remarkSchema,
  traffic_reset_day: z.number().int(t.users.invalidResetDay).min(0, t.users.invalidResetDay).max(28, t.users.invalidResetDay),
  limit_gib: z.number().refine((value) => value >= 0 && Number.isSafeInteger(Math.round(value * GiB)), t.users.invalidLimit),
  expiry: z.string().refine((value) => !value || Number.isFinite(Date.parse(value)) && Date.parse(value) >= 0, t.users.invalidDate),
  all_servers: z.boolean(),
  server_ids: z.array(z.number().int().positive()),
}).refine((value) => value.all_servers || value.server_ids.length > 0, { path: ['server_ids'], message: t.users.serversRequired })
export type UserFormValues = z.infer<typeof userSchema>
export function localDate(seconds: number) {
  if (!seconds) return ''
  const date = new Date(seconds * 1000)
  if (!Number.isFinite(date.getTime())) return ''
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 19)
}
export function userDefaults(user?: User): UserFormValues {
  return {
    name: user?.name ?? '', remark: user?.remark ?? '', limit_gib: (user?.traffic_limit ?? 0) / GiB, expiry: localDate(user?.expire_at ?? 0),
    traffic_reset_day: user?.traffic_reset_day ?? 0, all_servers: user?.all_servers ?? true, server_ids: user?.server_ids ?? [],
  }
}
export function userBody(values: UserFormValues): UserInput {
  return {
    name: values.name, remark: values.remark, traffic_reset_day: values.traffic_reset_day, traffic_limit: Math.round(values.limit_gib * GiB),
    expire_at: values.expiry ? Math.floor(Date.parse(values.expiry) / 1000) : 0,
    all_servers: values.all_servers, ...(values.all_servers ? {} : { server_ids: values.server_ids }),
  }
}
