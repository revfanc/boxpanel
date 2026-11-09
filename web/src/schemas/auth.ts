import { z } from 'zod'
import { zhCN } from '@/i18n/zh-CN'

export const loginSchema = z.object({
  username: z.string().regex(/^[a-zA-Z0-9_-]{1,32}$/, zhCN.login.usernameInvalid),
  password: z.string().min(1, zhCN.login.passwordRequired)
    .refine((value) => new TextEncoder().encode(value).length <= 1024, zhCN.login.passwordTooLong),
})
