import { z } from 'zod'
import { nameSchema, remarkSchema } from '@/schemas/settings'
import type { User, UserInput } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'
const GiB = 1024 ** 3
export const userSchema = z.object({
  name: nameSchema, remark: remarkSchema,
  limit_gib: z.number().refine((value) => value >= 0 && Number.isSafeInteger(Math.round(value * GiB)), t.users.invalidLimit),
  expiry: z.string().refine((value) => !value || Number.isFinite(Date.parse(value)) && Date.parse(value) >= 0, t.users.invalidDate),
})
export type UserFormValues = z.infer<typeof userSchema>
export function localDate(seconds: number) {
  if (!seconds) return ''
  const date = new Date(seconds * 1000)
  if (!Number.isFinite(date.getTime())) return ''
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 19)
}
export function userDefaults(user?: User): UserFormValues { return { name: user?.name ?? '', remark: user?.remark ?? '', limit_gib: (user?.traffic_limit ?? 0) / GiB, expiry: localDate(user?.expire_at ?? 0) } }
export function userBody(values: UserFormValues): UserInput { return { name: values.name, remark: values.remark, traffic_limit: Math.round(values.limit_gib * GiB), expire_at: values.expiry ? Math.floor(Date.parse(values.expiry) / 1000) : 0 } }
