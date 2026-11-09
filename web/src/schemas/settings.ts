import { z } from 'zod'
import { zhCN as t } from '@/i18n/zh-CN'
export const byteLength = (value: string) => new TextEncoder().encode(value).length
export const portSchema = z.number().int(t.inbounds.portInvalid).min(1, t.inbounds.portInvalid).max(65535, t.inbounds.portInvalid)
export const nameSchema = z.string().regex(/^[a-zA-Z0-9_-]{1,32}$/, t.login.usernameInvalid)
export const remarkSchema = z.string().refine((value) => byteLength(value) <= 1024, t.common.invalid)
export const hostSchema = z.string().refine((value) => {
  if (!value || /[^\x21-\x7e]|[\s/@?#\\]/.test(value)) return false
  if (value.includes(':')) { try { return new URL(`https://[${value}]`).hostname.startsWith('[') } catch { return false } }
  return value.length <= 253 && value.split('.').every((part) => /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/.test(part))
}, t.settings.hostInvalid)
export const settingsSchema = z.object({
  panel_name: z.string().refine((value) => value.trim().length > 0 && byteLength(value) <= 128 && !/[\r\n\0]/.test(value), t.settings.nameInvalid),
  public_host: hostSchema, web_port: portSchema,
  web_path: z.string().regex(/^[a-z0-9]{0,64}$/, t.settings.pathInvalid),
  sub_path: z.string().regex(/^[a-z0-9]{1,64}$/, t.settings.pathInvalid),
  block_bittorrent: z.boolean(),
}).refine((value) => value.web_path !== value.sub_path && !['api', 'assets', 'index'].includes(value.sub_path), { path: ['sub_path'], message: t.settings.pathInvalid })
export const usernameSchema = z.object({ username: nameSchema })
export const passwordSchema = z.object({
  old_password: z.string().min(1, t.login.passwordRequired).refine((value) => byteLength(value) <= 1024, t.login.passwordTooLong),
  new_password: z.string().refine((value) => Array.from(value).length >= 10 && byteLength(value) <= 1024, t.settings.passwordInvalid),
})
