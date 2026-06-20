import { describe, it, expect } from 'vitest'
import { canRunDanger, Role } from './danger'
import { decodeJwt } from './jwt'

describe('canRunDanger', () => {
  it('组成员被拒绝组范围与平台范围危险操作', () => {
    expect(canRunDanger(Role.Member, 'group')).toBe(false)
    expect(canRunDanger(Role.Member, 'platform')).toBe(false)
  })

  it('组管理员可执行组范围，但被拒平台范围', () => {
    expect(canRunDanger(Role.GroupAdmin, 'group')).toBe(true)
    expect(canRunDanger(Role.GroupAdmin, 'platform')).toBe(false)
  })

  it('平台管理员可执行所有范围', () => {
    expect(canRunDanger(Role.PlatformAdmin, 'group')).toBe(true)
    expect(canRunDanger(Role.PlatformAdmin, 'platform')).toBe(true)
  })

  it('未登录（role 为 null）一律被拒', () => {
    expect(canRunDanger(null, 'group')).toBe(false)
    expect(canRunDanger(null, 'platform')).toBe(false)
  })
})

/** 构造一个未签名的测试 JWT（仅 payload 有效）。 */
function makeJwt(payload: Record<string, unknown>): string {
  const b64url = (obj: Record<string, unknown>) =>
    btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url(payload)}.sig`
}

describe('decodeJwt', () => {
  it('解码 role/username/userId 声明', () => {
    const token = makeJwt({ userId: 7, username: 'admin', role: 10 })
    expect(decodeJwt(token)).toMatchObject({ userId: 7, username: 'admin', role: 10 })
  })

  it('token 缺失或格式错误返回 null', () => {
    expect(decodeJwt(null)).toBeNull()
    expect(decodeJwt(undefined)).toBeNull()
    expect(decodeJwt('not-a-jwt')).toBeNull()
    expect(decodeJwt('a.!!!.c')).toBeNull()
  })
})
