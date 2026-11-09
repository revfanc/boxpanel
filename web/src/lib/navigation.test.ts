import { describe, expect, it } from 'vitest'
import { loginPath, safeNext } from './navigation'

describe('safe login return path', () => {
  it.each([null, '', 'https://evil.example', '//evil.example', '/\\evil.example', '/%5Cevil.example', '/%2fevil.example', '/login', '/LOGIN', '/login/', '/users/../login?next=/users', '/%6cogin', '/bad%escape', '/\n/evil.example'])('rejects %s', (value) => {
    expect(safeNext(value)).toBe('/')
  })
  it('preserves local queries and anchors exactly once', () => {
    expect(safeNext('/users/3?view=links#share')).toBe('/users/3?view=links#share')
    expect(new URLSearchParams(loginPath('/users/3?a=%23#share').split('?')[1]).get('next')).toBe('/users/3?a=%23#share')
  })
})
