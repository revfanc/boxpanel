import { describe, expect, it } from 'vitest'
import { loginSchema } from './auth'

describe('login matches server validation', () => {
  it('does not impose a new-password policy on existing login credentials or trim passwords', () => {
    expect(loginSchema.parse({ username: 'admin-1', password: ' a ' }).password).toBe(' a ')
    expect(loginSchema.safeParse({ username: 'admin', password: 'x' }).success).toBe(true)
  })
  it('enforces byte limits, ASCII username rules and required fields', () => {
    for (const username of ['', 'a'.repeat(33), 'admin name', '用户']) {
      expect(loginSchema.safeParse({ username, password: 'test' }).success).toBe(false)
    }
    for (const password of ['', 'a'.repeat(1025), '密'.repeat(342)]) {
      expect(loginSchema.safeParse({ username: 'admin', password }).success).toBe(false)
    }
    expect(loginSchema.safeParse({ username: 'admin', password: '密'.repeat(341) }).success).toBe(true)
  })
})
