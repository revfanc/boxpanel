import { z } from 'zod'
import { zhCN as t } from '@/i18n/zh-CN'
import { hostSchema } from './settings'

export const serverNameSchema = z.object({
  name: z.string().refine((name) => {
    const length = Array.from(name.trim()).length
    return length >= 1 && length <= 64 && !/\p{Cc}/u.test(name)
  }, t.servers.invalidName).transform((name) => name.trim()),
})

export const serverSchema = serverNameSchema.extend({ public_host: hostSchema.optional() })
