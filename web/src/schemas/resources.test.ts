import { expect, it } from 'vitest'
import { inboundBody, inboundDefaults, inboundSchema } from './inbound'
import { rulesSchema } from './rules'
import { passwordSchema, settingsSchema } from './settings'
import { localDate, userBody, userDefaults, userSchema } from './user'
import { proxyUser, rules, settings } from '@/test/resources'

it('preserves Hysteria2 secrets on an ordinary edit and sends an explicit empty string only to clear obfuscation', () => {
  const input = inboundDefaults('hysteria2')
  expect(inboundBody(input).options).not.toHaveProperty('obfs_password')
  input.clear_obfs = true
  expect(inboundBody(input).options).toHaveProperty('obfs_password', '')
  input.start = 50000; input.end = 20000
  expect(inboundSchema.safeParse(input).success).toBe(false)
})
it('round-trips byte and second values without silently truncating an existing quota or expiry', () => {
  const user = { ...proxyUser, traffic_limit: 123456789, expire_at: 1800000123 }
  expect(userBody(userDefaults(user))).toMatchObject({ traffic_limit: user.traffic_limit, expire_at: user.expire_at })
  expect(localDate(0)).toBe('')
  expect(userSchema.safeParse({ ...userDefaults(user), limit_gib: Number.MAX_SAFE_INTEGER }).success).toBe(false)
})
it('validates DNS credentials, domain lists, pending tags and update-interval boundaries', () => {
  const input = { ...rules, direct_draft: '', proxy_draft: '' }
  expect(rulesSchema.safeParse({ ...input, dns: { ...rules.dns, remote: 'https://name:password@example.com/dns' } }).success).toBe(false)
  expect(rulesSchema.safeParse({ ...input, direct_draft: 'https://example.com' }).success).toBe(false)
  expect(rulesSchema.safeParse({ ...input, custom_proxy: ['127.0.0.1'] }).success).toBe(false)
  for (const update_interval of ['1h', '1h30m', '30d', '720h']) expect(rulesSchema.safeParse({ ...input, update_interval }).success).toBe(true)
  for (const update_interval of ['59m', '31d', '0d', 'bad']) expect(rulesSchema.safeParse({ ...input, update_interval }).success).toBe(false)
})
it('validates settings path collisions and password length in characters and bytes', () => {
  expect(settingsSchema.safeParse({ ...settings, sub_path: 'api' }).success).toBe(false)
  expect(settingsSchema.safeParse({ ...settings, web_path: settings.sub_path }).success).toBe(false)
  expect(passwordSchema.safeParse({ old_password: 'old', new_password: '🔐'.repeat(10) }).success).toBe(true)
  expect(passwordSchema.safeParse({ old_password: 'old', new_password: '🔐'.repeat(257) }).success).toBe(false)
})
